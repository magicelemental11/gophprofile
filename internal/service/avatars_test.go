package service

import (
	"bytes"
	"context"
	"image"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/imaging"
)

type env struct {
	repo  *fakeRepo
	store *fakeStore
	pub   *fakePublisher
	svc   *AvatarService
}

func newEnv() *env {
	e := &env{repo: newFakeRepo(), store: newFakeStore(), pub: &fakePublisher{}}
	e.svc = NewAvatarService(e.repo, e.store, e.pub, zap.NewNop(), 1<<20)
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
	require.Len(t, e.pub.events, 1)
	assert.Equal(t, domain.RoutingUploaded, e.pub.events[0].key)
	assert.Equal(t, domain.AvatarUploadEvent{AvatarID: a.ID, UserID: a.UserID, S3Key: a.S3Key}, e.pub.events[0].event)
}

func TestUploadRejects(t *testing.T) {
	e := newEnv()
	_, err := e.svc.Upload(context.Background(), "u", "a.txt", strings.NewReader("not an image"))
	assert.ErrorIs(t, err, domain.ErrInvalidFormat)

	_, err = e.svc.Upload(context.Background(), "u", "big.png", bytes.NewReader(make([]byte, 1<<20+1)))
	assert.ErrorIs(t, err, domain.ErrTooLarge)

	assert.Empty(t, e.repo.avatars)
	assert.Empty(t, e.pub.events)
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
	assert.Empty(t, e.pub.events)
}

func TestUploadPublishFailureStillSucceeds(t *testing.T) {
	e := newEnv()
	e.pub.err = errBoom
	a := e.upload(t, "u")
	assert.Equal(t, domain.UploadUploaded, a.UploadStatus)
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
	defer img.Body.Close()
	data, err := io.ReadAll(img.Body)
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
	_, err = e.svc.Open(context.Background(), a.ID, domain.Size100, "")
	assert.ErrorIs(t, err, errBoom)
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

	list, err := e.svc.List(context.Background(), "u")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, second.ID, list[0].ID)
}

func TestDelete(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	e.pub.events = nil

	require.NoError(t, e.svc.Delete(context.Background(), "u", a.ID))

	_, err := e.svc.Get(context.Background(), a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	require.Len(t, e.pub.events, 1)
	assert.Equal(t, domain.RoutingDeleted, e.pub.events[0].key)
	assert.Equal(t, domain.AvatarDeleteEvent{AvatarID: a.ID, S3Keys: a.AllKeys()}, e.pub.events[0].event)
}

func TestDeleteErrors(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	e.pub.events = nil

	assert.ErrorIs(t, e.svc.Delete(context.Background(), "intruder", a.ID), domain.ErrForbidden)
	assert.ErrorIs(t, e.svc.Delete(context.Background(), "u", "missing"), domain.ErrNotFound)

	e.repo.failNext = errBoom
	assert.ErrorIs(t, e.svc.Delete(context.Background(), "u", a.ID), errBoom)

	assert.Empty(t, e.pub.events)
	_, err := e.svc.Get(context.Background(), a.ID)
	assert.NoError(t, err, "avatar is kept")
}

func TestDeletePublishFailureStillSucceeds(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	e.pub.err = errBoom

	require.NoError(t, e.svc.Delete(context.Background(), "u", a.ID))
	_, err := e.svc.Get(context.Background(), a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
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

func TestDeletePublishesAfterClientGone(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	e.pub.events = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, e.svc.Delete(ctx, "u", a.ID))
	require.Len(t, e.pub.events, 1)
	assert.Equal(t, domain.RoutingDeleted, e.pub.events[0].key)
}

func TestUploadDeletedMidwayRemovesOriginal(t *testing.T) {
	e := newEnv()
	e.svc = NewAvatarService(&deleteOnMarkUploaded{fakeRepo: e.repo}, e.store, e.pub, zap.NewNop(), 1<<20)

	_, err := e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 10, 10)))
	require.ErrorIs(t, err, domain.ErrNotFound)
	assert.Empty(t, e.store.objects)
	assert.Empty(t, e.pub.events)
}

type deleteOnMarkUploaded struct {
	*fakeRepo
}

func (r *deleteOnMarkUploaded) MarkUploaded(ctx context.Context, id, etag string) error {
	if err := r.SoftDelete(ctx, id); err != nil {
		return err
	}
	return r.fakeRepo.MarkUploaded(ctx, id, etag)
}
