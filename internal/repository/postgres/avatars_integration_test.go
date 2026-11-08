//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func newAvatar(userID string) domain.Avatar {
	id := uuid.NewString()
	return domain.Avatar{
		ID:        id,
		UserID:    userID,
		FileName:  "cat.png",
		MimeType:  "image/png",
		SizeBytes: 42,
		Width:     640,
		Height:    480,
		S3Key:     domain.OriginalKey(id, domain.FormatPNG),
	}
}

func TestCreateGet(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	created, err := r.Create(ctx, newAvatar("gopher@example.com"))
	require.NoError(t, err)
	assert.Equal(t, domain.UploadUploading, created.UploadStatus)
	assert.Equal(t, domain.ProcessingPending, created.ProcessingStatus)
	assert.WithinDuration(t, time.Now(), created.CreatedAt, time.Minute)

	got, err := r.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created, got)
	assert.Empty(t, got.ThumbnailKeys)

	_, err = r.Get(ctx, uuid.NewString())
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = r.Get(ctx, "not-a-uuid")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestStatusTransitions(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	a, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)

	require.NoError(t, r.MarkUploaded(ctx, a.ID, "etag1"))
	require.NoError(t, r.SetProcessingStatus(ctx, a.ID, domain.ProcessingProcessing))
	got, err := r.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.UploadUploaded, got.UploadStatus)
	assert.Equal(t, "etag1", got.ETag)
	assert.Equal(t, domain.ProcessingProcessing, got.ProcessingStatus)

	require.NoError(t, r.SetUploadStatus(ctx, a.ID, domain.UploadFailed))
	got, err = r.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.UploadFailed, got.UploadStatus)

	assert.ErrorIs(t, r.MarkUploaded(ctx, uuid.NewString(), "x"), domain.ErrNotFound)
	assert.ErrorIs(t, r.SetProcessingStatus(ctx, uuid.NewString(), domain.ProcessingFailed), domain.ErrNotFound)
}

func TestLatestAndList(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	_, err := r.GetLatestByUser(ctx, "u")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	first, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)
	require.NoError(t, r.MarkUploaded(ctx, first.ID, "e"))
	second, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)
	_, err = r.Create(ctx, newAvatar("other"))
	require.NoError(t, err)

	latest, err := r.GetLatestByUser(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, first.ID, latest.ID, "second is not uploaded yet")

	require.NoError(t, r.MarkUploaded(ctx, second.ID, "e"))
	latest, err = r.GetLatestByUser(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, second.ID, latest.ID)

	list, err := r.ListByUser(ctx, "u")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, second.ID, list[0].ID)

	empty, err := r.ListByUser(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestSoftDelete(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	a, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)
	require.NoError(t, r.MarkUploaded(ctx, a.ID, "e"))

	require.NoError(t, r.SoftDelete(ctx, a.ID))
	_, err = r.Get(ctx, a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = r.GetLatestByUser(ctx, "u")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	list, err := r.ListByUser(ctx, "u")
	require.NoError(t, err)
	assert.Empty(t, list)

	assert.ErrorIs(t, r.SoftDelete(ctx, a.ID), domain.ErrNotFound)

	var deleted bool
	require.NoError(t, r.pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM avatars WHERE id = $1`, a.ID).Scan(&deleted))
	assert.True(t, deleted)
}

func TestCompleteProcessing(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	a, err := r.Create(ctx, newAvatar("u"))
	require.NoError(t, err)
	keys := map[domain.Size]string{domain.Size100: "k100", domain.Size300: "k300"}
	msg := uuid.NewString()

	ok, err := r.CompleteProcessing(ctx, a.ID, keys, msg)
	require.NoError(t, err)
	assert.True(t, ok)

	got, err := r.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ProcessingCompleted, got.ProcessingStatus)
	assert.Equal(t, keys, got.ThumbnailKeys)

	processed, err := r.IsProcessed(ctx, msg)
	require.NoError(t, err)
	assert.True(t, processed)

	ok, err = r.CompleteProcessing(ctx, a.ID, keys, msg)
	require.NoError(t, err, "repeated message is not an error")
	assert.True(t, ok)

	require.NoError(t, r.SoftDelete(ctx, a.ID))
	deletedMsg := uuid.NewString()
	ok, err = r.CompleteProcessing(ctx, a.ID, keys, deletedMsg)
	require.NoError(t, err)
	assert.False(t, ok, "deleted avatar is not updated")
	processed, err = r.IsProcessed(ctx, deletedMsg)
	require.NoError(t, err)
	assert.False(t, processed, "message stays open so cleanup can be retried")
}

func TestProcessedMessages(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	msg := uuid.NewString()

	ok, err := r.IsProcessed(ctx, msg)
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, r.MarkProcessed(ctx, msg))
	require.NoError(t, r.MarkProcessed(ctx, msg))
	ok, err = r.IsProcessed(ctx, msg)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = r.IsProcessed(ctx, "garbage")
	require.NoError(t, err)
	assert.False(t, ok)
}
