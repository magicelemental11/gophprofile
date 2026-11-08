// Package config loads server and worker settings from flags and environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// S3 holds the object storage settings.
type S3 struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Common holds the settings shared by the server and the worker.
type Common struct {
	DatabaseDSN string
	AMQPURL     string
	LogLevel    string
	S3          S3
}

// Server holds the HTTP server settings.
type Server struct {
	Common
	Address       string
	MaxUploadSize int64
	WebDir        string
}

// Worker holds the background worker settings.
type Worker struct {
	Common
	Prefetch   int
	MaxRetries int
}

type binding struct{ flag, env string }

var commonBindings = []binding{
	{"d", "DATABASE_DSN"},
	{"amqp", "AMQP_URL"},
	{"log-level", "LOG_LEVEL"},
	{"s3-endpoint", "S3_ENDPOINT"},
	{"s3-access-key", "S3_ACCESS_KEY"},
	{"s3-secret-key", "S3_SECRET_KEY"},
	{"s3-bucket", "S3_BUCKET"},
	{"s3-use-ssl", "S3_USE_SSL"},
}

// LoadServer parses args into a Server config. Precedence: flag, environment variable, default.
func LoadServer(args []string) (Server, error) {
	var cfg Server
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	registerCommon(fs, &cfg.Common)
	fs.StringVar(&cfg.Address, "a", ":8080", "listen address (env ADDRESS)")
	fs.Int64Var(&cfg.MaxUploadSize, "max-upload-size", 10<<20, "maximum upload size in bytes (env MAX_UPLOAD_SIZE)")
	fs.StringVar(&cfg.WebDir, "web-dir", "./web", "web interface directory (env WEB_DIR)")
	bindings := append([]binding{
		{"a", "ADDRESS"},
		{"max-upload-size", "MAX_UPLOAD_SIZE"},
		{"web-dir", "WEB_DIR"},
	}, commonBindings...)
	if err := parse(fs, args, bindings); err != nil {
		return Server{}, err
	}
	if err := cfg.validate(); err != nil {
		return Server{}, err
	}
	if cfg.MaxUploadSize <= 0 {
		return Server{}, errors.New("max upload size must be positive")
	}
	return cfg, nil
}

// LoadWorker parses args into a Worker config. Precedence: flag, environment variable, default.
func LoadWorker(args []string) (Worker, error) {
	var cfg Worker
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	registerCommon(fs, &cfg.Common)
	fs.IntVar(&cfg.Prefetch, "prefetch", 10, "messages taken from the broker at once (env WORKER_PREFETCH)")
	fs.IntVar(&cfg.MaxRetries, "max-retries", 5, "processing attempts per message (env WORKER_MAX_RETRIES)")
	bindings := append([]binding{
		{"prefetch", "WORKER_PREFETCH"},
		{"max-retries", "WORKER_MAX_RETRIES"},
	}, commonBindings...)
	if err := parse(fs, args, bindings); err != nil {
		return Worker{}, err
	}
	if err := cfg.validate(); err != nil {
		return Worker{}, err
	}
	if cfg.Prefetch <= 0 || cfg.MaxRetries <= 0 {
		return Worker{}, errors.New("prefetch and max retries must be positive")
	}
	return cfg, nil
}

func registerCommon(fs *flag.FlagSet, c *Common) {
	fs.StringVar(&c.DatabaseDSN, "d", "", "PostgreSQL DSN, required (env DATABASE_DSN)")
	fs.StringVar(&c.AMQPURL, "amqp", "", "RabbitMQ URL, required (env AMQP_URL)")
	fs.StringVar(&c.LogLevel, "log-level", "info", "log level (env LOG_LEVEL)")
	fs.StringVar(&c.S3.Endpoint, "s3-endpoint", "", "S3 endpoint host:port, required (env S3_ENDPOINT)")
	fs.StringVar(&c.S3.AccessKey, "s3-access-key", "", "S3 access key, required (env S3_ACCESS_KEY)")
	fs.StringVar(&c.S3.SecretKey, "s3-secret-key", "", "S3 secret key, required (env S3_SECRET_KEY)")
	fs.StringVar(&c.S3.Bucket, "s3-bucket", "avatars", "S3 bucket (env S3_BUCKET)")
	fs.BoolVar(&c.S3.UseSSL, "s3-use-ssl", false, "use HTTPS for S3 (env S3_USE_SSL)")
}

func parse(fs *flag.FlagSet, args []string, bindings []binding) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, b := range bindings {
		if set[b.flag] {
			continue
		}
		v, ok := os.LookupEnv(b.env)
		if !ok {
			continue
		}
		if v == "" {
			return fmt.Errorf("%s is set but empty", b.env)
		}
		if err := fs.Set(b.flag, v); err != nil {
			return fmt.Errorf("%s: %w", b.env, err)
		}
	}
	return nil
}

func (c Common) validate() error {
	switch {
	case c.DatabaseDSN == "":
		return errors.New("database DSN is required (-d or DATABASE_DSN)")
	case c.S3.Endpoint == "" || c.S3.AccessKey == "" || c.S3.SecretKey == "":
		return errors.New("S3 endpoint and credentials are required (-s3-endpoint, -s3-access-key, -s3-secret-key)")
	case c.AMQPURL == "":
		return errors.New("RabbitMQ URL is required (-amqp or AMQP_URL)")
	}
	return nil
}
