// Package worker turns broker messages into processing calls with retries and exponential backoff.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/service"
)

const (
	baseDelay = 500 * time.Millisecond
	maxDelay  = 30 * time.Second
)

// Message is a broker message reduced to what the worker needs: id, routing key and body.
type Message struct {
	ID         string
	RoutingKey string
	Body       []byte
}

// Processor does the actual work for a message.
type Processor interface {
	Process(ctx context.Context, messageID string, ev domain.AvatarProcessEvent) error
	DeleteFiles(ctx context.Context, messageID string, ev domain.AvatarDeleteEvent) error
	MarkFailed(ctx context.Context, avatarID string) error
}

// Metrics records message handling for monitoring.
type Metrics interface {
	ObserveMessage(ctx context.Context, operation string, outcome domain.Outcome, d time.Duration)
	Retried(operation string)
	DeadLettered(operation string)
}

// Operation names reported to Metrics.
const (
	operationProcess = domain.OperationProcess
	operationDelete  = domain.OperationDelete
	operationUnknown = "unknown"
)

// Worker dispatches messages to the processor.
type Worker struct {
	proc       Processor
	metrics    Metrics
	log        *slog.Logger
	maxRetries int
	sleep      func(ctx context.Context, d time.Duration) error
}

// New returns a worker that tries each message up to maxRetries times.
func New(proc Processor, metrics Metrics, log *slog.Logger, maxRetries int) *Worker {
	return &Worker{proc: proc, metrics: metrics, log: log, maxRetries: maxRetries, sleep: sleepContext}
}

// Handle processes one message. A returned error means the message is given up on
// and should go to the dead-letter queue.
func (w *Worker) Handle(ctx context.Context, m Message) error {
	start := time.Now()
	op := operationOf(m.RoutingKey)
	log := w.log.With(slog.String("message_id", m.ID), slog.String("routing_key", m.RoutingKey))
	err := w.handle(ctx, m, op, log)
	w.metrics.ObserveMessage(ctx, op, domain.OutcomeOf(err), time.Since(start))
	if err != nil {
		w.metrics.DeadLettered(op)
		log.ErrorContext(ctx, "message failed", slog.Any("error", err))
	}
	return err
}

func operationOf(routingKey string) string {
	switch routingKey {
	case domain.RoutingUploaded, domain.RoutingProcess:
		return operationProcess
	case domain.RoutingDeleted:
		return operationDelete
	default:
		return operationUnknown
	}
}

func (w *Worker) handle(ctx context.Context, m Message, op string, log *slog.Logger) error {
	var ev domain.AvatarProcessEvent
	switch m.RoutingKey {
	case domain.RoutingUploaded:
		var up domain.AvatarUploadEvent
		if err := decode(m.Body, &up); err != nil {
			return err
		}
		ev = domain.AvatarProcessEvent{AvatarID: up.AvatarID, Operations: domain.DefaultOperations()}
	case domain.RoutingProcess:
		if err := decode(m.Body, &ev); err != nil {
			return err
		}
	case domain.RoutingDeleted:
		return w.deleteFiles(ctx, m, op, log)
	default:
		return fmt.Errorf("%w: unknown routing key %q", service.ErrPermanent, m.RoutingKey)
	}

	err := w.retry(ctx, op, log, func() error { return w.proc.Process(ctx, m.ID, ev) })
	if err != nil {
		if errFail := w.proc.MarkFailed(context.WithoutCancel(ctx), ev.AvatarID); errFail != nil {
			log.ErrorContext(ctx, "mark avatar failed", slog.String("avatar_id", ev.AvatarID), slog.Any("error", errFail))
		}
		return err
	}
	log.InfoContext(ctx, "avatar processed", slog.String("avatar_id", ev.AvatarID))
	return nil
}

func (w *Worker) deleteFiles(ctx context.Context, m Message, op string, log *slog.Logger) error {
	var ev domain.AvatarDeleteEvent
	if err := decode(m.Body, &ev); err != nil {
		return err
	}
	if err := w.retry(ctx, op, log, func() error { return w.proc.DeleteFiles(ctx, m.ID, ev) }); err != nil {
		return err
	}
	log.InfoContext(ctx, "avatar files deleted", slog.String("avatar_id", ev.AvatarID), slog.Int("keys", len(ev.S3Keys)))
	return nil
}

func decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: decode message: %w", service.ErrPermanent, err)
	}
	return nil
}

func (w *Worker) retry(ctx context.Context, op string, log *slog.Logger, fn func() error) error {
	var err error
	for attempt := range w.maxRetries {
		if err = fn(); err == nil || errors.Is(err, service.ErrPermanent) {
			return err
		}
		if attempt == w.maxRetries-1 {
			break
		}
		delay := backoff(attempt)
		w.metrics.Retried(op)
		log.WarnContext(ctx, "attempt failed, retrying", slog.Int("attempt", attempt+1), slog.Duration("delay", delay), slog.Any("error", err))
		trace.SpanFromContext(ctx).AddEvent("retry", trace.WithAttributes(
			attribute.Int("attempt", attempt+1),
			attribute.String("delay", delay.String()),
			attribute.String("error", err.Error()),
		))
		if errSleep := w.sleep(ctx, delay); errSleep != nil {
			return errors.Join(err, errSleep)
		}
	}
	return err
}

func backoff(attempt int) time.Duration {
	d := baseDelay << attempt
	if d <= 0 || d > maxDelay {
		return maxDelay
	}
	return d
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
