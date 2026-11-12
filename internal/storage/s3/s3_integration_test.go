//go:build integration

package s3

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/domain"
)

var testCfg config.S3

func TestMain(m *testing.M) {
	ctx := context.Background()
	c, err := tcminio.Run(ctx, "pgsty/minio:RELEASE.2026-08-04T00-00-00Z",
		tcminio.WithUsername("gopher"), tcminio.WithPassword("gopher-secret"))
	if err != nil {
		log.Fatalf("start minio: %v", err)
	}
	endpoint, err := c.ConnectionString(ctx)
	if err != nil {
		log.Fatalf("endpoint: %v", err)
	}
	testCfg = config.S3{Endpoint: endpoint, AccessKey: "gopher", SecretKey: "gopher-secret", Bucket: "avatars"}
	code := m.Run()
	_ = c.Terminate(ctx)
	os.Exit(code)
}

func TestPutGetDelete(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, testCfg)
	require.NoError(t, err)
	require.NoError(t, s.Ping(ctx))

	_, err = New(ctx, testCfg)
	require.NoError(t, err, "existing bucket is reused")

	data := []byte("image bytes")
	etag, err := s.Put(ctx, "originals/a.png", bytes.NewReader(data), int64(len(data)), "image/png")
	require.NoError(t, err)
	assert.NotEmpty(t, etag)

	rc, err := s.Get(ctx, "originals/a.png")
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, data, got)

	require.NoError(t, s.Delete(ctx, []string{"originals/a.png", "thumbnails/missing.jpg"}))
	_, err = s.Get(ctx, "originals/a.png")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPingMissingBucket(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, testCfg)
	require.NoError(t, err)
	s.bucket = "nope"
	assert.Error(t, s.Ping(ctx))
}
