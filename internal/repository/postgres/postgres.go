// Package postgres stores avatar metadata and processed message ids in PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/telemetry"

	"github.com/magicelemental11/gophprofile/migrations"
)

type db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Repository is a PostgreSQL-backed store, safe for concurrent use.
type Repository struct {
	pool *pgxpool.Pool
	db   db
}

// New connects to the database at dsn. Queries made within a recorded span get child spans.
func New(ctx context.Context, dsn string) (*Repository, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Repository{pool: pool, db: pool}, nil
}

// Close releases all database connections.
func (r *Repository) Close() {
	r.pool.Close()
}

// Ping checks that the database answers.
func (r *Repository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// Migrate applies the embedded migrations.
func (r *Repository) Migrate() error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	db := stdlib.OpenDBFromPool(r.pool)
	defer db.Close()
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

const tracerName = "github.com/magicelemental11/gophprofile/internal/repository/postgres"

func startTx(ctx context.Context, op, avatarID string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, "postgres."+op, trace.WithAttributes(
		semconv.DBSystemNamePostgreSQL,
		attribute.String("avatar_id", avatarID),
	))
}

func endTx(span trace.Span, err error) {
	telemetry.End(span, err, func(err error) bool { return errors.Is(err, domain.ErrNotFound) })
}
