//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func collect(t *testing.T, r *Repository, limit int) []domain.Event {
	t.Helper()
	var got []domain.Event
	_, err := r.PublishPending(context.Background(), limit, func(_ context.Context, ev domain.Event) error {
		got = append(got, ev)
		return nil
	})
	require.NoError(t, err)
	return got
}

func TestOutbox(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	a, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)

	uploaded, deleted := testEvent(), testEvent()
	deleted.RoutingKey = domain.RoutingDeleted
	require.NoError(t, r.MarkUploaded(ctx, a.ID, "e", uploaded))
	require.NoError(t, r.SoftDelete(ctx, a.ID, deleted))
	assert.ErrorIs(t, r.SoftDelete(ctx, a.ID, testEvent()), domain.ErrNotFound)

	errPub := errors.New("broker down")
	n, err := r.PublishPending(ctx, 10, func(context.Context, domain.Event) error { return errPub })
	assert.ErrorIs(t, err, errPub)
	assert.Zero(t, n)

	got := collect(t, r, 1)
	require.Len(t, got, 1)
	assert.Equal(t, uploaded.ID, got[0].ID)
	assert.JSONEq(t, string(uploaded.Payload), string(got[0].Payload))

	got = collect(t, r, 10)
	require.Len(t, got, 1, "missing avatar does not queue an event")
	assert.Equal(t, deleted.ID, got[0].ID)
	assert.Equal(t, domain.RoutingDeleted, got[0].RoutingKey)

	assert.Empty(t, collect(t, r, 10))
}

func TestOutboxSkipsLockedEvents(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for range 2 {
		a, err := r.Create(ctx, newAvatar("u"))
		require.NoError(t, err)
		require.NoError(t, r.MarkUploaded(ctx, a.ID, "e", testEvent()))
	}

	var inner []domain.Event
	_, err := r.PublishPending(ctx, 1, func(context.Context, domain.Event) error {
		inner = collect(t, r, 10)
		return nil
	})
	require.NoError(t, err)
	assert.Len(t, inner, 1, "a concurrent relay skips the locked event")
	assert.Empty(t, collect(t, r, 10))
}

func TestOutboxRollsBackWithUpdate(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	a, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)

	ev := testEvent()
	ev.ID = "not-a-uuid"
	require.Error(t, r.MarkUploaded(ctx, a.ID, "e", ev))
	got, err := r.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.UploadUploading, got.UploadStatus, "status is not changed without its event")
	assert.Empty(t, collect(t, r, 10))
}
