//go:build integration

package postgres

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("gopher"),
		tcpostgres.WithPassword("gopher"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Fatalf("start postgres: %v", err)
	}
	testDSN, err = c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("dsn: %v", err)
	}
	code := m.Run()
	_ = c.Terminate(ctx)
	os.Exit(code)
}

func newTestRepo(t *testing.T) *Repository {
	t.Helper()
	ctx := context.Background()
	r, err := New(ctx, testDSN)
	require.NoError(t, err)
	t.Cleanup(r.Close)
	require.NoError(t, r.Migrate())
	_, err = r.pool.Exec(ctx, `TRUNCATE avatars, processed_messages, outbox`)
	require.NoError(t, err)
	return r
}

func TestMigrateIdempotent(t *testing.T) {
	r := newTestRepo(t)
	require.NoError(t, r.Migrate())
	require.NoError(t, r.Ping(context.Background()))

	var n int
	err := r.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('avatars', 'processed_messages', 'outbox')`).Scan(&n)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}
