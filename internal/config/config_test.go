package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var requiredArgs = []string{
	"-d", "postgres://localhost/db",
	"-s3-endpoint", "localhost:9000",
	"-s3-access-key", "key",
	"-s3-secret-key", "secret",
	"-amqp", "amqp://localhost",
}

func TestLoadServerDefaults(t *testing.T) {
	cfg, err := LoadServer(requiredArgs)
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.Address)
	assert.Equal(t, int64(10<<20), cfg.MaxUploadSize)
	assert.Equal(t, "./web", cfg.WebDir)
	assert.Equal(t, "avatars", cfg.S3.Bucket)
	assert.False(t, cfg.S3.UseSSL)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "postgres://localhost/db", cfg.DatabaseDSN)
	assert.Equal(t, "amqp://localhost", cfg.AMQPURL)
}

func TestLoadServerEnv(t *testing.T) {
	t.Setenv("ADDRESS", ":9999")
	t.Setenv("DATABASE_DSN", "postgres://env/db")
	t.Setenv("S3_ENDPOINT", "minio:9000")
	t.Setenv("S3_ACCESS_KEY", "ak")
	t.Setenv("S3_SECRET_KEY", "sk")
	t.Setenv("S3_USE_SSL", "true")
	t.Setenv("AMQP_URL", "amqp://env")
	t.Setenv("MAX_UPLOAD_SIZE", "1024")

	cfg, err := LoadServer(nil)
	require.NoError(t, err)
	assert.Equal(t, ":9999", cfg.Address)
	assert.Equal(t, "postgres://env/db", cfg.DatabaseDSN)
	assert.Equal(t, S3{Endpoint: "minio:9000", AccessKey: "ak", SecretKey: "sk", Bucket: "avatars", UseSSL: true}, cfg.S3)
	assert.Equal(t, int64(1024), cfg.MaxUploadSize)
}

func TestLoadServerFlagBeatsEnv(t *testing.T) {
	t.Setenv("ADDRESS", ":9999")
	cfg, err := LoadServer(append([]string{"-a", ":7000"}, requiredArgs...))
	require.NoError(t, err)
	assert.Equal(t, ":7000", cfg.Address)
}

func TestLoadServerErrors(t *testing.T) {
	tests := map[string]struct {
		args []string
		env  map[string]string
	}{
		"missing dsn":  {args: []string{"-s3-endpoint", "e", "-s3-access-key", "a", "-s3-secret-key", "s", "-amqp", "u"}},
		"missing s3":   {args: []string{"-d", "dsn", "-amqp", "u"}},
		"missing amqp": {args: []string{"-d", "dsn", "-s3-endpoint", "e", "-s3-access-key", "a", "-s3-secret-key", "s"}},
		"empty env":    {args: requiredArgs, env: map[string]string{"ADDRESS": ""}},
		"bad int env":  {args: requiredArgs, env: map[string]string{"MAX_UPLOAD_SIZE": "big"}},
		"bad size":     {args: append([]string{"-max-upload-size", "0"}, requiredArgs...)},
		"unknown flag": {args: []string{"-nope"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := LoadServer(tt.args)
			assert.Error(t, err)
		})
	}
}

func TestLoadWorker(t *testing.T) {
	t.Setenv("WORKER_PREFETCH", "3")
	cfg, err := LoadWorker(append([]string{"-max-retries", "7"}, requiredArgs...))
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Prefetch)
	assert.Equal(t, 7, cfg.MaxRetries)
	assert.Equal(t, "postgres://localhost/db", cfg.DatabaseDSN)
}

func TestLoadWorkerErrors(t *testing.T) {
	_, err := LoadWorker(append([]string{"-prefetch", "0"}, requiredArgs...))
	assert.Error(t, err)
	_, err = LoadWorker(nil)
	assert.Error(t, err)
}
