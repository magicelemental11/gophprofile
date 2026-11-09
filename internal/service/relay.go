package service

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const relayBatch = 100

// Relay publishes events queued in the outbox. Delivery is at least once: the worker
// deduplicates messages by id.
type Relay struct {
	repo     OutboxRepository
	pub      EventPublisher
	log      *zap.Logger
	interval time.Duration
}

// NewRelay returns a relay that checks the outbox every interval.
func NewRelay(repo OutboxRepository, pub EventPublisher, log *zap.Logger, interval time.Duration) *Relay {
	return &Relay{repo: repo, pub: pub, log: log, interval: interval}
}

// Run publishes queued events until ctx is done. Failed events stay queued for the next round.
func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		r.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Relay) drain(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := r.repo.PublishPending(ctx, relayBatch, r.pub.Publish)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("publish outbox events", zap.Int("published", n), zap.Error(err))
			}
			return
		}
		if n < relayBatch {
			return
		}
	}
}
