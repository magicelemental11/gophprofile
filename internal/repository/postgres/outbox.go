package postgres

import (
	"context"
	"fmt"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// PublishPending passes up to limit queued events, oldest first, to publish and removes the
// published ones. The events stay locked until it returns, so concurrent relays skip them.
// It stops at the first failed event and returns how many were published; an event published
// but not removed because of a later database error is sent again.
func (r *Repository) PublishPending(ctx context.Context, limit int, publish func(context.Context, domain.Event) error) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, routing_key, payload FROM outbox
		ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}
	var events []domain.Event
	for rows.Next() {
		var ev domain.Event
		if err := rows.Scan(&ev.ID, &ev.RoutingKey, &ev.Payload); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}

	var ids []string
	var pubErr error
	for _, ev := range events {
		if pubErr = publish(ctx, ev); pubErr != nil {
			pubErr = fmt.Errorf("publish event %s: %w", ev.ID, pubErr)
			break
		}
		ids = append(ids, ev.ID)
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
