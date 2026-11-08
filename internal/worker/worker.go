// Package worker turns broker messages into processing calls with retries and exponential backoff.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/service"
)

const (
	baseDelay = 500 * time.Millisecond
	maxDelay  = 30 * time.Second
)

// Message is a delivered broker message.
type Message struct {
	ID         string
	RoutingKey string
	Body       []byte
}

// Processor does the actual work for a message.
type Processor interface {
	Process(ctx context.Context, messageID string, ev domain.AvatarProcessEvent) error
	MarkFailed(ctx context.Context, avatarID string) error
}

// Worker dispatches messages to the processor.
type Worker struct {
	proc       Processor
	log        *zap.Logger
	maxRetries int
	sleep      func(ctx context.Context, d time.Duration) error
}

// New returns a worker that tries each message up to maxRetries times.
func New(proc Processor, log *zap.Logger, maxRetries int) *Worker {
	return &Worker{proc: proc, log: log, maxRetries: maxRetries, sleep: sleepContext}
}

// Handle processes one message. A returned error means the message is given up on
// and should go to the dead-letter queue.
func (w *Worker) Handle(ctx context.Context, m Message) error {
	log := w.log.With(zap.String("message_id", m.ID), zap.String("routing_key", m.RoutingKey))
	err := w.handle(ctx, m, log)
	if err != nil {
		log.Error("message failed", zap.Error(err))
	}
	return err
}

func (w *Worker) handle(ctx context.Context, m Message, log *zap.Logger) error {
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
	default:
		return fmt.Errorf("%w: unknown routing key %q", service.ErrPermanent, m.RoutingKey)
	}

	err := w.retry(ctx, log, func() error { return w.proc.Process(ctx, m.ID, ev) })
	if err != nil {
		if errFail := w.proc.MarkFailed(context.WithoutCancel(ctx), ev.AvatarID); errFail != nil {
			log.Error("mark avatar failed", zap.String("avatar_id", ev.AvatarID), zap.Error(errFail))
		}
		return err
	}
	log.Info("avatar processed", zap.String("avatar_id", ev.AvatarID))
	return nil
}

func decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: decode message: %w", service.ErrPermanent, err)
	}
	return nil
}

func (w *Worker) retry(ctx context.Context, log *zap.Logger, fn func() error) error {
	var err error
	for attempt := range w.maxRetries {
		if err = fn(); err == nil || errors.Is(err, service.ErrPermanent) {
			return err
		}
		if attempt == w.maxRetries-1 {
			break
		}
		delay := backoff(attempt)
		log.Warn("attempt failed, retrying", zap.Int("attempt", attempt+1), zap.Duration("delay", delay), zap.Error(err))
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
