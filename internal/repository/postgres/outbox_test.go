package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/telemetry/telemetrytest"
)

const otherID = "3a9d7c1e-5b2f-4e8a-9c6d-1f0e2b4a7d58"

const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func outboxRows() [][]any {
	return [][]any{
		{messageID, domain.RoutingUploaded, []byte(`{"avatar_id":"a"}`), map[string]string{"traceparent": traceparent}},
		{otherID, domain.RoutingDeleted, []byte(`{"avatar_id":"b"}`), map[string]string{}},
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

func TestPublishPendingRestoresTraceContext(t *testing.T) {
	telemetrytest.Record(t)
	r, db := newFakeRepo(t)
	db.expect(result{rows: outboxRows()})
	db.expect(result{affected: 2})

	var traces []trace.SpanContext
	_, err := r.PublishPending(context.Background(), 10, func(ctx context.Context, _ domain.Event) error {
		traces = append(traces, trace.SpanContextFromContext(ctx))
		return nil
	})

	require.NoError(t, err)
	require.Len(t, traces, 2)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traces[0].TraceID().String())
	assert.True(t, traces[0].IsRemote())
	assert.False(t, traces[1].IsValid(), "an event queued outside a trace starts none")
	assert.Regexp(t, `SELECT id, routing_key, payload, trace_context FROM outbox`, db.calls[0].sql)
}
