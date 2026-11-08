//go:build integration

package service_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/repository/postgres"
	"github.com/magicelemental11/gophprofile/internal/service"
	"github.com/magicelemental11/gophprofile/internal/storage/s3"
)

type capturePublisher struct {
	mu     sync.Mutex
	events map[string][]any
}

func (p *capturePublisher) Publish(_ context.Context, key string, event any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events[key] = append(p.events[key], event)
	return nil
}

func (p *capturePublisher) Ping(context.Context) error { return nil }

func startDeps(t *testing.T) (*postgres.Repository, *s3.Storage) {
	t.Helper()
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("gopher"),
		tcpostgres.WithPassword("gopher"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	repo, err := postgres.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(repo.Close)
	require.NoError(t, repo.Migrate())

	mc, err := tcminio.Run(ctx, "pgsty/minio:RELEASE.2026-08-04T00-00-00Z",
		tcminio.WithUsername("gopher"), tcminio.WithPassword("gopher-secret"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mc.Terminate(context.Background()) })
	endpoint, err := mc.ConnectionString(ctx)
	require.NoError(t, err)
	store, err := s3.New(ctx, config.S3{Endpoint: endpoint, AccessKey: "gopher", SecretKey: "gopher-secret", Bucket: "avatars"})
	require.NoError(t, err)
	return repo, store
}

func pngImage(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for x := range 400 {
		for y := range 200 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func assertObject(t *testing.T, store *s3.Storage, key string, exists bool) {
	t.Helper()
	body, err := store.Get(context.Background(), key)
	if exists {
		require.NoError(t, err, key)
		_ = body.Close()
		return
	}
	assert.ErrorIs(t, err, domain.ErrNotFound, key)
}

func TestAvatarLifecycle(t *testing.T) {
	ctx := context.Background()
	repo, store := startDeps(t)
	pub := &capturePublisher{events: map[string][]any{}}
	svc := service.NewAvatarService(repo, store, pub, zap.NewNop(), 10<<20)
	proc := service.NewProcessor(repo, store, zap.NewNop())

	a, err := svc.Upload(ctx, "gopher@example.com", "cat.png", bytes.NewReader(pngImage(t)))
	require.NoError(t, err)
	require.Len(t, pub.events[domain.RoutingUploaded], 1)

	ev := domain.AvatarProcessEvent{AvatarID: a.ID, Operations: domain.DefaultOperations()}
	require.NoError(t, proc.Process(ctx, uuid.NewString(), ev))
	got, err := svc.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusReady, got.Status())
	for _, k := range a.AllKeys() {
		assertObject(t, store, k, true)
	}

	assert.ErrorIs(t, svc.Delete(ctx, "intruder", a.ID), domain.ErrForbidden)
	require.NoError(t, svc.Delete(ctx, "gopher@example.com", a.ID))
	require.Len(t, pub.events[domain.RoutingDeleted], 1)
	del, ok := pub.events[domain.RoutingDeleted][0].(domain.AvatarDeleteEvent)
	require.True(t, ok)
	require.NoError(t, proc.DeleteFiles(ctx, uuid.NewString(), del))

	for _, k := range a.AllKeys() {
		assertObject(t, store, k, false)
	}
	_, err = svc.Get(ctx, a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	list, err := svc.List(ctx, "gopher@example.com")
	require.NoError(t, err)
	assert.Empty(t, list)
	_, err = svc.OpenLatest(ctx, "gopher@example.com", domain.SizeOriginal, "")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
