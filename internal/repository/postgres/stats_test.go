package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func TestStats(t *testing.T) {
	r, db := newFakeRepo(t)
	db.expect(result{rows: [][]any{{"a@x", int64(100)}, {"b@x", int64(50)}}})
	db.expect(result{rows: [][]any{{"completed", int64(2)}, {"pending", int64(1)}}})
	db.expect(result{rows: [][]any{{int64(3)}}})

	s, err := r.Stats(context.Background())

	require.NoError(t, err)
	assert.Equal(t, domain.Stats{
		StorageBytes:  map[string]int64{"a@x": 100, "b@x": 50},
		Avatars:       map[domain.ProcessingStatus]int64{domain.ProcessingCompleted: 2, domain.ProcessingPending: 1},
		OutboxPending: 3,
	}, s)
	for _, c := range db.calls[:2] {
		assert.Contains(t, c.sql, "deleted_at IS NULL AND upload_status = 'uploaded'")
	}
	db.lastSQL(t, `SELECT count\(\*\) FROM outbox`)
}

func TestStatsErrors(t *testing.T) {
	for n := range 3 {
		r, db := newFakeRepo(t)
		for range n {
			db.expect(result{})
		}
		db.expect(result{err: errDB})
		_, err := r.Stats(context.Background())
		assert.ErrorIs(t, err, errDB, n)
	}
}
