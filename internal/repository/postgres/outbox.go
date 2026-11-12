package postgres

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// PublishPending passes up to limit queued events, oldest first, to publish and removes the
// published ones. The events stay locked until it returns, so concurrent relays skip them.
// Each event is published with the trace context of the operation that queued it.
// It stops at the first failed event and returns how many were published; an event published
// but not removed because of a later database error is sent again.
func (r *Repository) PublishPending(ctx context.Context, limit int, publish func(context.Context, domain.Event) error) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, routing_key, payload, trace_context FROM outbox
		ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}
	var events []queued
	for rows.Next() {
		var q queued
		if err := rows.Scan(&q.ev.ID, &q.ev.RoutingKey, &q.ev.Payload, &q.trace); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}

	var ids []string
	var pubErr error
	for _, q := range events {
		if pubErr = publish(extractTrace(ctx, q.trace), q.ev); pubErr != nil {
			pubErr = fmt.Errorf("publish event %s: %w", q.ev.ID, pubErr)
			break
		}
		ids = append(ids, q.ev.ID)
	}
	if len(ids) == 0 {
		return 0, pubErr
	}
	if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("remove published events: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(ids), pubErr
}

type queued struct {
	ev    domain.Event
	trace map[string]string
}

func injectTrace(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}

func extractTrace(ctx context.Context, carrier map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}
