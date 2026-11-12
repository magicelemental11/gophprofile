// Command worker processes avatar events from RabbitMQ.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/magicelemental11/gophprofile/internal/broker/rabbitmq"
	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/logger"
	"github.com/magicelemental11/gophprofile/internal/metrics"
	"github.com/magicelemental11/gophprofile/internal/repository/postgres"
	"github.com/magicelemental11/gophprofile/internal/service"
	"github.com/magicelemental11/gophprofile/internal/storage/s3"
	"github.com/magicelemental11/gophprofile/internal/telemetry"
	"github.com/magicelemental11/gophprofile/internal/worker"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	serviceName  = "gophprofile-worker"
	flushTimeout = 5 * time.Second
)

func main() {
	cfg, err := config.LoadWorker(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	lg, err := logger.New(os.Stdout, cfg.LogLevel, serviceName)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err = run(ctx, cfg, lg)
	stop()
	if err != nil {
		lg.Error("worker stopped", slog.Any("error", err))
		os.Exit(1)
	}
	lg.Info("worker stopped")
}

func run(ctx context.Context, cfg config.Worker, lg *slog.Logger) error {
	flushTraces, err := telemetry.Setup(ctx, telemetry.Config{ServiceName: serviceName, Version: version, Endpoint: cfg.OTLPEndpoint})
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
		defer cancel()
		if err := flushTraces(flushCtx); err != nil {
			lg.WarnContext(flushCtx, "flush traces", slog.Any("error", err))
		}
	}()

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

	m := metrics.New()
	if err := m.Register(metrics.NewPoolCollector(func() metrics.PoolStat { return repo.PoolStat() })); err != nil {
		return err
	}
	metricsCtx, stopMetrics := context.WithCancel(ctx)
	defer stopMetrics()
	metricsErr := make(chan error, 1)
	go func() { metricsErr <- metrics.Serve(metricsCtx, cfg.MetricsAddress, m.Handler()) }()

	w := worker.New(service.NewProcessor(repo, store, lg), m, lg, cfg.MaxRetries)
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
	lg.InfoContext(ctx, "worker started", slog.Any("queues", queues))

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
	case runErr = <-metricsErr:
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
