// Package logger builds the zap logger used by the server and the worker.
package logger

import (
	"fmt"

	"go.uber.org/zap"
)

// New returns a production JSON logger writing at the given level.
func New(level string) (*zap.Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		return nil, fmt.Errorf("parse log level: %w", err)
	}
	cfg := zap.NewProductionConfig()
	cfg.Level = lvl
	return cfg.Build()
}
