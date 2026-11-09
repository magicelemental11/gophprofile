package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

const otherID = "3a9d7c1e-5b2f-4e8a-9c6d-1f0e2b4a7d58"

func outboxRows() [][]any {
	return [][]any{
		{messageID, domain.RoutingUploaded, []byte(`{"avatar_id":"a"}`)},
		{otherID, domain.RoutingDeleted, []byte(`{"avatar_id":"b"}`)},
	}
}

func TestPublishPending(t *testing.T) {
	ctx := context.Background()

	t.Run("publishes and removes", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{rows: outboxRows()})
		db.expect(result{affected: 2})

		var got []domain.Event
		n, err := r.PublishPending(ctx, 10, func(_ context.Context, ev domain.Event) error {
			got = append(got, ev)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, []domain.Event{
			{ID: messageID, RoutingKey: domain.RoutingUploaded, Payload: []byte(`{"avatar_id":"a"}`)},
			{ID: otherID, RoutingKey: domain.RoutingDeleted, Payload: []byte(`{"avatar_id":"b"}`)},
		}, got)
		assert.Regexp(t, `FOR UPDATE SKIP LOCKED`, db.calls[0].sql)
		assert.Equal(t, []any{10}, db.calls[0].args)
		db.lastSQL(t, `DELETE FROM outbox WHERE id = ANY\(\$1\)`)
		assert.Equal(t, []any{[]string{messageID, otherID}}, db.calls[1].args)
		assert.True(t, db.committed)
	})

	t.Run("stops at failed event", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{rows: outboxRows()})
		db.expect(result{affected: 1})
		errPub := errors.New("broker down")

		n, err := r.PublishPending(ctx, 10, func(_ context.Context, ev domain.Event) error {
			if ev.ID == otherID {
				return errPub
			}
			return nil
		})
		assert.ErrorIs(t, err, errPub)
		assert.Equal(t, 1, n)
		assert.Equal(t, []any{[]string{messageID}}, db.calls[1].args)
		assert.True(t, db.committed)
	})

	t.Run("nothing published", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{})

		n, err := r.PublishPending(ctx, 10, func(context.Context, domain.Event) error { return nil })
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Len(t, db.calls, 1)
		assert.False(t, db.committed)
	})

	t.Run("database errors", func(t *testing.T) {
		ok := func(context.Context, domain.Event) error { return nil }

		r, db := newFakeRepo(t)
		db.beginErr = errDB
		_, err := r.PublishPending(ctx, 10, ok)
		assert.ErrorIs(t, err, errDB)

		r, db = newFakeRepo(t)
		db.expect(result{err: errDB})
		_, err = r.PublishPending(ctx, 10, ok)
		assert.ErrorIs(t, err, errDB)

		r, db = newFakeRepo(t)
		db.expect(result{rows: outboxRows(), rowErr: errDB})
		_, err = r.PublishPending(ctx, 10, ok)
		assert.ErrorIs(t, err, errDB)

		r, db = newFakeRepo(t)
		db.expect(result{rows: outboxRows()})
		db.expect(result{err: errDB})
		_, err = r.PublishPending(ctx, 10, ok)
		assert.ErrorIs(t, err, errDB)
		assert.True(t, db.rolled)

		r, db = newFakeRepo(t)
		db.expect(result{rows: outboxRows()})
		db.expect(result{affected: 2})
		db.commitErr = errDB
		_, err = r.PublishPending(ctx, 10, ok)
		assert.ErrorIs(t, err, errDB)
	})
}
