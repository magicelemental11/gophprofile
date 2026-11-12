// Command worker processes avatar events from RabbitMQ.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/broker/rabbitmq"
	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/logger"
	"github.com/magicelemental11/gophprofile/internal/repository/postgres"
	"github.com/magicelemental11/gophprofile/internal/service"
	"github.com/magicelemental11/gophprofile/internal/storage/s3"
	"github.com/magicelemental11/gophprofile/internal/worker"
)

func main() {
	cfg, err := config.LoadWorker(os.Args[1:])
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
		lg.Fatal("worker stopped", zap.Error(err))
	}
	lg.Info("worker stopped")
}

func run(ctx context.Context, cfg config.Worker, lg *zap.Logger) error {
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

	w := worker.New(service.NewProcessor(repo, store, lg), lg, cfg.MaxRetries)
	handle := func(ctx context.Context, m rabbitmq.Message) error {
		return w.Handle(ctx, worker.Message{ID: m.ID, RoutingKey: m.RoutingKey, Body: m.Body})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closed := broker.NotifyClose()
	queues := []string{rabbitmq.QueueProcessing, rabbitmq.QueueDeletion}
	errCh := make(chan error, len(queues))
	for _, q := range queues {
		go func() { errCh <- broker.Consume(ctx, q, cfg.Prefetch, handle) }()
	}
	lg.Info("worker started", zap.Strings("queues", queues))

	var runErr error
	select {
	case <-ctx.Done():
	case amqpErr, ok := <-closed:
		runErr = errors.New("broker connection closed")
		if ok {
			runErr = fmt.Errorf("broker connection lost: %w", amqpErr)
		}
	case runErr = <-errCh:
		queues = queues[1:]
	}
	cancel()
	for range queues {
		if err := <-errCh; err != nil && runErr == nil {
			runErr = err
		}
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	return nil
}
