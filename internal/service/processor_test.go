package service

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/imaging"
)

func newProcessor(e *env) *Processor {
	return NewProcessor(e.repo, e.store, zap.NewNop())
}

func processEvent(id string) domain.AvatarProcessEvent {
	return domain.AvatarProcessEvent{AvatarID: id, Operations: domain.DefaultOperations()}
}

func TestProcessCreatesThumbnails(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	msg := uuid.NewString()

	require.NoError(t, newProcessor(e).Process(context.Background(), msg, processEvent(a.ID)))

	got, err := e.repo.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ProcessingCompleted, got.ProcessingStatus)
	assert.Equal(t, map[domain.Size]string{
		domain.Size100: domain.ThumbnailKey(a.ID, domain.Size100),
		domain.Size300: domain.ThumbnailKey(a.ID, domain.Size300),
	}, got.ThumbnailKeys)
	assert.True(t, e.repo.processed[msg])

	for _, s := range domain.ThumbnailSizes {
		rc, err := e.store.Get(context.Background(), domain.ThumbnailKey(a.ID, s))
		require.NoError(t, err)
		data, _ := io.ReadAll(rc)
		info, err := imaging.Inspect(data)
		require.NoError(t, err)
		w, h := s.Dimensions()
		assert.Equal(t, imaging.Info{Format: domain.FormatJPEG, Width: w, Height: h}, info)
		assert.Equal(t, "image/jpeg", e.store.types[domain.ThumbnailKey(a.ID, s)])
	}
}

func TestProcessIsIdempotent(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	p := newProcessor(e)
	msg := uuid.NewString()

	require.NoError(t, p.Process(context.Background(), msg, processEvent(a.ID)))
	puts := e.store.puts
	before, _ := e.repo.Get(context.Background(), a.ID)

	require.NoError(t, p.Process(context.Background(), msg, processEvent(a.ID)), "same message")
	require.NoError(t, p.Process(context.Background(), uuid.NewString(), processEvent(a.ID)), "duplicate event, already completed")

	after, _ := e.repo.Get(context.Background(), a.ID)
	assert.Equal(t, puts, e.store.puts, "no extra uploads")
	assert.Equal(t, before.UpdatedAt, after.UpdatedAt)
}

func TestProcessSkipsMissingAvatar(t *testing.T) {
	e := newEnv()
	assert.NoError(t, newProcessor(e).Process(context.Background(), uuid.NewString(), processEvent("gone")))
	assert.Zero(t, e.store.puts)
}

func TestProcessPermanentErrors(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	p := newProcessor(e)

	bad := domain.AvatarProcessEvent{AvatarID: a.ID, Operations: []domain.ProcessingOp{{Type: "rotate", Size: domain.Size100}}}
	assert.ErrorIs(t, p.Process(context.Background(), uuid.NewString(), bad), ErrPermanent)

	bad.Operations = []domain.ProcessingOp{{Type: domain.OpResize, Size: "7x7"}}
	assert.ErrorIs(t, p.Process(context.Background(), uuid.NewString(), bad), ErrPermanent)

	assert.ErrorIs(t, p.Process(context.Background(), "", processEvent(a.ID)), ErrPermanent)

	e.store.objects[a.S3Key] = []byte("corrupted")
	assert.ErrorIs(t, p.Process(context.Background(), uuid.NewString(), processEvent(a.ID)), ErrPermanent)

	e.store.putFails = 1
	failed, err := e.svc.Upload(context.Background(), "u", "x.png", bytes.NewReader(pngBytes(t, 5, 5)))
	require.Error(t, err)
	_ = failed
	for id, av := range e.repo.avatars {
		if av.UploadStatus == domain.UploadFailed {
			assert.ErrorIs(t, p.Process(context.Background(), uuid.NewString(), processEvent(id)), ErrPermanent)
		}
	}
}

func TestProcessTransientErrors(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	p := newProcessor(e)

	e.store.putFails = 1
	err := p.Process(context.Background(), uuid.NewString(), processEvent(a.ID))
	require.ErrorIs(t, err, errBoom)
	assert.NotErrorIs(t, err, ErrPermanent)

	e.store.getErr = errBoom
	assert.ErrorIs(t, p.Process(context.Background(), uuid.NewString(), processEvent(a.ID)), errBoom)
	e.store.getErr = nil

	e.repo.failNext = errBoom
	assert.ErrorIs(t, p.Process(context.Background(), uuid.NewString(), processEvent(a.ID)), errBoom)

	require.NoError(t, p.Process(context.Background(), uuid.NewString(), processEvent(a.ID)), "retry succeeds")
}

func TestProcessDeletedMeanwhileRemovesThumbnails(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	repo := &deleteOnComplete{fakeRepo: e.repo}
	p := NewProcessor(repo, e.store, zap.NewNop())

	require.NoError(t, p.Process(context.Background(), uuid.NewString(), processEvent(a.ID)))
	for _, s := range domain.ThumbnailSizes {
		assert.False(t, e.store.has(domain.ThumbnailKey(a.ID, s)))
	}
}

type deleteOnComplete struct {
	*fakeRepo
}

func (r *deleteOnComplete) CompleteProcessing(ctx context.Context, id string, keys map[domain.Size]string, msg string) (bool, error) {
	if err := r.SoftDelete(ctx, id); err != nil {
		return false, err
	}
	return r.fakeRepo.CompleteProcessing(ctx, id, keys, msg)
}

func TestMarkFailed(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	p := newProcessor(e)

	require.NoError(t, p.MarkFailed(context.Background(), a.ID))
	got, _ := e.repo.Get(context.Background(), a.ID)
	assert.Equal(t, domain.StatusFailed, got.Status())

	assert.NoError(t, p.MarkFailed(context.Background(), "gone"), "missing avatar is fine")
}
