// Command worker processes avatar events from RabbitMQ.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/logger"
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

	lg.Info("worker started")
	<-ctx.Done()
	lg.Info("worker stopped")
}
