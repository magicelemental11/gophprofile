//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func TestStatsCountsUploadedAvatars(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, user := range []string{"a@x", "a@x", "b@x", "c@x"} {
		a, err := r.Create(ctx, newAvatar(user))
		require.NoError(t, err)
		if user == "c@x" {
			continue
		}
		require.NoError(t, r.MarkUploaded(ctx, a.ID, "e", testEvent()))
	}
	deleted, err := r.Create(ctx, newAvatar("b@x"))
	require.NoError(t, err)
	require.NoError(t, r.MarkUploaded(ctx, deleted.ID, "e", testEvent()))
	require.NoError(t, r.SoftDelete(ctx, deleted.ID, testEvent()))

	s, err := r.Stats(ctx)

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"a@x": 84, "b@x": 42}, s.StorageBytes)
	assert.Equal(t, map[domain.ProcessingStatus]int64{domain.ProcessingPending: 3}, s.Avatars)
	assert.Equal(t, int64(5), s.OutboxPending)
	assert.Positive(t, r.PoolStat().TotalConns())
}
