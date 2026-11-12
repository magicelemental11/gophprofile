package service

import (
	"bytes"
	"context"
	"image"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/imaging"
)

type env struct {
	repo  *fakeRepo
	store *fakeStore
	svc   *AvatarService
}

func newEnv() *env {
	e := &env{repo: newFakeRepo(), store: newFakeStore()}
	e.svc = NewAvatarService(e.repo, e.store, zap.NewNop(), 1<<20)
	return e
}

func (e *env) upload(t *testing.T, userID string) domain.Avatar {
	t.Helper()
	a, err := e.svc.Upload(context.Background(), userID, "cat.png", bytes.NewReader(pngBytes(t, 120, 80)))
	require.NoError(t, err)
	return a
}

func TestUpload(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "gopher@example.com")

	assert.Equal(t, "gopher@example.com", a.UserID)
	assert.Equal(t, "cat.png", a.FileName)
	assert.Equal(t, "image/png", a.MimeType)
	assert.Equal(t, 120, a.Width)
	assert.Equal(t, 80, a.Height)
	assert.Equal(t, domain.OriginalKey(a.ID, domain.FormatPNG), a.S3Key)
	assert.Equal(t, domain.UploadUploaded, a.UploadStatus)
	assert.Equal(t, domain.StatusProcessing, a.Status())

	assert.True(t, e.store.has(a.S3Key))
	assert.Equal(t, "image/png", e.store.types[a.S3Key])
	events := e.repo.queued()
	require.Len(t, events, 1)
	assert.Equal(t, domain.RoutingUploaded, events[0].RoutingKey)
	assert.NoError(t, uuid.Validate(events[0].ID))
	assert.Equal(t, domain.AvatarUploadEvent{AvatarID: a.ID, UserID: a.UserID, S3Key: a.S3Key},
		decodeEvent[domain.AvatarUploadEvent](t, events[0]))
}

func TestUploadRejects(t *testing.T) {
	e := newEnv()
	_, err := e.svc.Upload(context.Background(), "u", "a.txt", strings.NewReader("not an image"))
	assert.ErrorIs(t, err, domain.ErrInvalidFormat)

	_, err = e.svc.Upload(context.Background(), "u", "big.png", bytes.NewReader(make([]byte, 1<<20+1)))
	assert.ErrorIs(t, err, domain.ErrTooLarge)

	assert.Empty(t, e.repo.avatars)
	assert.Empty(t, e.repo.queued())
}

func TestUploadCleansFileName(t *testing.T) {
	e := newEnv()
	a, err := e.svc.Upload(context.Background(), "u", `..\..\secret\cat.png`, bytes.NewReader(pngBytes(t, 10, 10)))
	require.NoError(t, err)
	assert.Equal(t, "cat.png", a.FileName)
	assert.Equal(t, "cat.png", e.repo.avatars[a.ID].FileName)
}

func TestUploadStorageFailure(t *testing.T) {
	e := newEnv()
	e.store.putFails = 1
	_, err := e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.ErrorIs(t, err, errBoom)

	require.Len(t, e.repo.avatars, 1)
	for _, a := range e.repo.avatars {
		assert.Equal(t, domain.UploadFailed, a.UploadStatus)
	}
	assert.Empty(t, e.repo.queued())
}

func TestUploadMarkUploadedFailureCleansUp(t *testing.T) {
	e := newEnv()
	e.svc = NewAvatarService(&failMarkUploaded{fakeRepo: e.repo, err: errBoom}, e.store, zap.NewNop(), 1<<20)

	_, err := e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.ErrorIs(t, err, errBoom)

	assert.Equal(t, 1, e.store.puts)
	assert.Empty(t, e.store.objects, "original is removed")
	require.Len(t, e.repo.avatars, 1)
	for _, a := range e.repo.avatars {
		assert.Equal(t, domain.UploadFailed, a.UploadStatus)
	}
	assert.Empty(t, e.repo.queued())
}

func TestUploadCleanupAfterClientGone(t *testing.T) {
	e := newEnv()
	ctx, cancel := context.WithCancel(context.Background())
	e.svc = NewAvatarService(&failMarkUploaded{fakeRepo: e.repo, err: context.Canceled, before: cancel}, e.store, zap.NewNop(), 1<<20)

	_, err := e.svc.Upload(ctx, "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, e.store.objects)
	for _, a := range e.repo.avatars {
		assert.Equal(t, domain.UploadFailed, a.UploadStatus)
	}
}

type failMarkUploaded struct {
	*fakeRepo
	err    error
	before func()
}

func (r *failMarkUploaded) MarkUploaded(context.Context, string, string, domain.Event) error {
	if r.before != nil {
		r.before()
	}
	return r.err
}

func TestUploadRepoFailure(t *testing.T) {
	e := newEnv()
	e.repo.failNext = errBoom
	_, err := e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	assert.ErrorIs(t, err, errBoom)
	assert.Zero(t, e.store.puts)
}

func TestUploadReadError(t *testing.T) {
	e := newEnv()
	_, err := e.svc.Upload(context.Background(), "u", "x.png", io.MultiReader(strings.NewReader("ab"), errReader{}))
	assert.ErrorIs(t, err, errBoom)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBoom }

func readImage(t *testing.T, img Image) imaging.Info {
	t.Helper()
	body, err := img.Open(context.Background())
	require.NoError(t, err)
	defer body.Close()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	info, err := imaging.Inspect(data)
	require.NoError(t, err)
	assert.Equal(t, info.Format.MIME(), img.ContentType)
	return info
}

func TestOpenOriginal(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")

	img, err := e.svc.Open(context.Background(), a.ID, domain.SizeOriginal, "")
	require.NoError(t, err)
	assert.Equal(t, imaging.Info{Format: domain.FormatPNG, Width: 120, Height: 80}, readImage(t, img))
	assert.Equal(t, `"etag-`+a.S3Key+`-original-png"`, img.ETag)
}

func TestOpenConvertsFormat(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")

	img, err := e.svc.Open(context.Background(), a.ID, domain.SizeOriginal, domain.FormatWebP)
	require.NoError(t, err)
	assert.Equal(t, imaging.Info{Format: domain.FormatWebP, Width: 120, Height: 80}, readImage(t, img))
}

func TestOpenThumbnailOnTheFly(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")

	img, err := e.svc.Open(context.Background(), a.ID, domain.Size100, "")
	require.NoError(t, err)
	assert.Equal(t, imaging.Info{Format: domain.FormatJPEG, Width: 100, Height: 100}, readImage(t, img))
}

func TestOpenStoredThumbnail(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	key := domain.ThumbnailKey(a.ID, domain.Size300)
	var buf bytes.Buffer
	require.NoError(t, imaging.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 300, 300)), domain.FormatJPEG))
	_, err := e.store.Put(context.Background(), key, &buf, int64(buf.Len()), "image/jpeg")
	require.NoError(t, err)
	_, err = e.repo.CompleteProcessing(context.Background(), a.ID, map[domain.Size]string{domain.Size300: key}, "m")
	require.NoError(t, err)
	e.store.objects[a.S3Key] = nil

	img, err := e.svc.Open(context.Background(), a.ID, domain.Size300, domain.FormatPNG)
	require.NoError(t, err)
	assert.Equal(t, imaging.Info{Format: domain.FormatPNG, Width: 300, Height: 300}, readImage(t, img))
}

func TestOpenErrors(t *testing.T) {
	e := newEnv()
	_, err := e.svc.Open(context.Background(), "missing", domain.SizeOriginal, "")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	e.store.putFails = 1
	_, err = e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.Error(t, err)
	for id := range e.repo.avatars {
		_, err = e.svc.Open(context.Background(), id, domain.SizeOriginal, "")
		assert.ErrorIs(t, err, domain.ErrNotFound, "failed upload is not served")
	}

	a := e.upload(t, "u")
	e.store.getErr = errBoom
	img, err := e.svc.Open(context.Background(), a.ID, domain.Size100, "")
	require.NoError(t, err, "metadata is enough to describe the image")
	assert.Equal(t, "image/jpeg", img.ContentType)
	_, err = img.Open(context.Background())
	assert.ErrorIs(t, err, errBoom)

	e.store.getErr = nil
	e.store.objects[a.S3Key] = []byte("broken")
	img, err = e.svc.Open(context.Background(), a.ID, domain.Size100, "")
	require.NoError(t, err)
	_, err = img.Open(context.Background())
	assert.ErrorIs(t, err, domain.ErrInvalidFormat)
}

func TestOpenLatestGetList(t *testing.T) {
	e := newEnv()
	_, err := e.svc.OpenLatest(context.Background(), "u", domain.SizeOriginal, "")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	e.upload(t, "u")
	second := e.upload(t, "u")

	img, err := e.svc.OpenLatest(context.Background(), "u", domain.SizeOriginal, "")
	require.NoError(t, err)
	assert.Contains(t, img.ETag, second.S3Key)
	readImage(t, img)

	got, err := e.svc.Get(context.Background(), second.ID)
	require.NoError(t, err)
	assert.Equal(t, second.ID, got.ID)

	e.store.putFails = 1
	_, err = e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.Error(t, err)

	list, err := e.svc.List(context.Background(), "u")
	require.NoError(t, err)
	require.Len(t, list, 2, "a failed upload is not listed")
	assert.Equal(t, second.ID, list[0].ID)
}

func TestDelete(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	e.repo.outbox = nil

	require.NoError(t, e.svc.Delete(context.Background(), "u", a.ID))

	_, err := e.svc.Get(context.Background(), a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	events := e.repo.queued()
	require.Len(t, events, 1)
	assert.Equal(t, domain.RoutingDeleted, events[0].RoutingKey)
	assert.Equal(t, domain.AvatarDeleteEvent{AvatarID: a.ID, S3Keys: a.AllKeys()},
		decodeEvent[domain.AvatarDeleteEvent](t, events[0]))
}

func TestDeleteErrors(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	e.repo.outbox = nil

	assert.ErrorIs(t, e.svc.Delete(context.Background(), "intruder", a.ID), domain.ErrForbidden)
	assert.ErrorIs(t, e.svc.Delete(context.Background(), "u", "missing"), domain.ErrNotFound)

	e.repo.failNext = errBoom
	assert.ErrorIs(t, e.svc.Delete(context.Background(), "u", a.ID), errBoom)

	assert.Empty(t, e.repo.queued())
	_, err := e.svc.Get(context.Background(), a.ID)
	assert.NoError(t, err, "avatar is kept")
}

func TestDeleteCurrent(t *testing.T) {
	e := newEnv()
	older := e.upload(t, "u")
	newer := e.upload(t, "u")

	require.NoError(t, e.svc.DeleteCurrent(context.Background(), "u", "u"))
	_, err := e.svc.Get(context.Background(), newer.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = e.svc.Get(context.Background(), older.ID)
	require.NoError(t, err)

	require.NoError(t, e.svc.DeleteCurrent(context.Background(), "u", "u"))
	assert.ErrorIs(t, e.svc.DeleteCurrent(context.Background(), "u", "u"), domain.ErrNotFound)
}

func TestDeleteCurrentForbidden(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")

	assert.ErrorIs(t, e.svc.DeleteCurrent(context.Background(), "intruder", "u"), domain.ErrForbidden)
	_, err := e.svc.Get(context.Background(), a.ID)
	assert.NoError(t, err)
}

func TestUploadDeletedMidwayRemovesOriginal(t *testing.T) {
	e := newEnv()
	e.svc = NewAvatarService(&deleteOnMarkUploaded{fakeRepo: e.repo}, e.store, zap.NewNop(), 1<<20)

	_, err := e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.ErrorIs(t, err, domain.ErrNotFound)
	assert.Empty(t, e.store.objects)
	assert.Empty(t, e.repo.queued())
}

type deleteOnMarkUploaded struct {
	*fakeRepo
}

func (r *deleteOnMarkUploaded) MarkUploaded(ctx context.Context, id, etag string, ev domain.Event) error {
	r.mu.Lock()
	r.deleted[id] = true
	r.mu.Unlock()
	return r.fakeRepo.MarkUploaded(ctx, id, etag, ev)
}
