// Command server runs the GophProfile HTTP API and web interface.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/broker/rabbitmq"
	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/handlers"
	"github.com/magicelemental11/gophprofile/internal/logger"
	"github.com/magicelemental11/gophprofile/internal/repository/postgres"
	"github.com/magicelemental11/gophprofile/internal/storage/s3"
)

const shutdownTimeout = 10 * time.Second

func main() {
	cfg, err := config.LoadServer(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	lg, err := logger.New(cfg.LogLevel)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = lg.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, lg); err != nil {
		lg.Fatal("server stopped", zap.Error(err))
	}
}

func run(ctx context.Context, cfg config.Server, lg *zap.Logger) error {
	repo, err := postgres.New(ctx, cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer repo.Close()
	if err := repo.Migrate(); err != nil {
		return err
	}
	store, err := s3.New(ctx, cfg.S3)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	broker, err := rabbitmq.Dial(cfg.AMQPURL)
	if err != nil {
		return fmt.Errorf("broker: %w", err)
	}
	defer func() { _ = broker.Close() }()

	router := handlers.NewRouter(handlers.Deps{
		Logger: lg,
		Health: map[string]handlers.Pinger{"database": repo, "storage": store, "broker": broker},
	})
	srv := &http.Server{
		Addr:              cfg.Address,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	lg.Info("server started", zap.String("address", cfg.Address))

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	lg.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
