//go:build integration

package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

var testURL string

func TestMain(m *testing.M) {
	ctx := context.Background()
	c, err := tcrabbitmq.Run(ctx, "rabbitmq:4-management-alpine")
	if err != nil {
		log.Fatalf("start rabbitmq: %v", err)
	}
	testURL, err = c.AmqpURL(ctx)
	if err != nil {
		log.Fatalf("url: %v", err)
	}
	code := m.Run()
	_ = c.Terminate(ctx)
	os.Exit(code)
}

func TestPublishConsume(t *testing.T) {
	b, err := Dial(testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.Ping(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- b.Consume(ctx, QueueProcessing, 2, func(_ context.Context, m Message) error {
			got <- m
			return nil
		})
	}()

	ev := domain.AvatarUploadEvent{AvatarID: "a1", UserID: "u", S3Key: "originals/a1.png"}
	body, err := json.Marshal(ev)
	require.NoError(t, err)
	msgID := uuid.NewString()
	require.NoError(t, b.Publish(context.Background(), domain.Event{ID: msgID, RoutingKey: domain.RoutingUploaded, Payload: body}))

	select {
	case m := <-got:
		assert.Equal(t, domain.RoutingUploaded, m.RoutingKey)
		assert.Equal(t, msgID, m.ID)
		var decoded domain.AvatarUploadEvent
		require.NoError(t, json.Unmarshal(m.Body, &decoded))
		assert.Equal(t, ev, decoded)
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered")
	}

	cancel()
	require.NoError(t, <-done)
}

func TestFailedMessageGoesToDeadLetter(t *testing.T) {
	b, err := Dial(testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = b.Consume(ctx, QueueDeletion, 1, func(context.Context, Message) error { return errors.New("boom") })
	}()
	dead := make(chan Message, 1)
	go func() {
		_ = b.Consume(ctx, QueueDead, 1, func(_ context.Context, m Message) error {
			dead <- m
			return nil
		})
	}()

	ev := domain.Event{ID: uuid.NewString(), RoutingKey: domain.RoutingDeleted, Payload: []byte(`{"avatar_id":"a2"}`)}
	require.NoError(t, b.Publish(context.Background(), ev))
	select {
	case m := <-dead:
		assert.Equal(t, domain.RoutingDeleted, m.RoutingKey)
	case <-time.After(5 * time.Second):
		t.Fatal("message not dead-lettered")
	}
}

func TestDialBadURL(t *testing.T) {
	_, err := Dial("amqp://nobody:nothing@127.0.0.1:1/")
	assert.Error(t, err)
}
