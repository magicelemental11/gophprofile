package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func queueEvents(repo *fakeRepo, n int) []domain.Event {
	events := make([]domain.Event, n)
	for i := range events {
		events[i] = domain.Event{ID: uuid.NewString(), RoutingKey: domain.RoutingUploaded, Payload: []byte(`{}`)}
		repo.queue(events[i])
	}
	return events
}

func TestRelayDrainsFullBatches(t *testing.T) {
	repo, pub := newFakeRepo(), &fakePublisher{}
	events := queueEvents(repo, relayBatch*2+1)

	NewRelay(repo, pub, zap.NewNop(), time.Hour).drain(context.Background())

	assert.Equal(t, events, pub.published())
	assert.Empty(t, repo.queued())
}

func TestRelayKeepsFailedEvents(t *testing.T) {
	repo, pub := newFakeRepo(), &fakePublisher{fails: 1}
	events := queueEvents(repo, 2)
	r := NewRelay(repo, pub, zap.NewNop(), time.Hour)

	r.drain(context.Background())
	assert.Empty(t, pub.published())
	assert.Equal(t, events, repo.queued(), "event stays queued after a failed publish")

	r.drain(context.Background())
	assert.Equal(t, events, pub.published())
	assert.Empty(t, repo.queued())
}

func TestRelayRepoFailure(t *testing.T) {
	repo, pub := newFakeRepo(), &fakePublisher{}
	queueEvents(repo, 1)
	repo.failNext = errBoom

	NewRelay(repo, pub, zap.NewNop(), time.Hour).drain(context.Background())
	assert.Empty(t, pub.published())
	assert.Len(t, repo.queued(), 1)
}

func TestRelayRunPublishesUntilCanceled(t *testing.T) {
	repo, pub := newFakeRepo(), &fakePublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewRelay(repo, pub, zap.NewNop(), 5*time.Millisecond).Run(ctx)
		close(done)
	}()

	events := queueEvents(repo, 3)
	require.Eventually(t, func() bool { return len(pub.published()) == 3 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, events, pub.published())

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop")
	}
}

func TestUploadEventReachesBroker(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	pub := &fakePublisher{}

	NewRelay(e.repo, pub, zap.NewNop(), time.Hour).drain(context.Background())

	events := pub.published()
	require.Len(t, events, 1)
	assert.Equal(t, a.ID, decodeEvent[domain.AvatarUploadEvent](t, events[0]).AvatarID)
}
