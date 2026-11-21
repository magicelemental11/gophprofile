// Package s3 stores avatar files in an S3-compatible object storage such as MinIO.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/telemetry"
)

// Storage keeps objects in one bucket.
type Storage struct {
	client *minio.Client
	bucket string
}

// New connects to the storage and creates the bucket when it is missing.
func New(ctx context.Context, cfg config.S3) (*Storage, error) {
	transport, err := minio.DefaultTransport(cfg.UseSSL)
	if err != nil {
		return nil, fmt.Errorf("create s3 transport: %w", err)
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:    cfg.UseSSL,
		Transport: otelhttp.NewTransport(transport, otelhttp.WithFilter(hasParentSpan)),
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	s := &Storage{client: client, bucket: cfg.Bucket}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			if exists, errExists := client.BucketExists(ctx, cfg.Bucket); errExists != nil || !exists {
				return nil, fmt.Errorf("create bucket: %w", err)
			}
		}
	}
	return s, nil
}

// Put uploads an object and returns its ETag.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	ctx, span := s.startSpan(ctx, "put_object", semconv.AWSS3Key(key), attribute.Int64("file_size", size))
	etag, err := s.put(ctx, key, r, size, contentType)
	endSpan(span, err)
	return etag, err
}

func (s *Storage) put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	info, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("put %s: %w", key, err)
	}
	return info.ETag, nil
}

// Get opens an object for reading; a missing object is domain.ErrNotFound.
func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	ctx, span := s.startSpan(ctx, "get_object", semconv.AWSS3Key(key))
	body, err := s.get(ctx, key)
	endSpan(span, err)
	return body, err
}

func (s *Storage) get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, fmt.Errorf("get %s: %w", key, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return obj, nil
}

// Delete removes objects; keys that do not exist are ignored.
func (s *Storage) Delete(ctx context.Context, keys []string) error {
	ctx, span := s.startSpan(ctx, "remove_objects", attribute.Int("keys", len(keys)))
	err := s.remove(ctx, keys)
	endSpan(span, err)
	return err
}

func (s *Storage) remove(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
	}
	return nil
}

// Ping checks that the bucket is reachable.
func (s *Storage) Ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", s.bucket, err)
	}
	if !ok {
		return fmt.Errorf("bucket %s does not exist", s.bucket)
	}
	return nil
}

const tracerName = "github.com/magicelemental11/gophprofile/internal/storage/s3"

func (s *Storage) startSpan(ctx context.Context, op string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, "s3."+op, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(attrs, semconv.AWSS3Bucket(s.bucket))...))
}

func endSpan(span trace.Span, err error) {
	telemetry.End(span, err, func(err error) bool { return errors.Is(err, domain.ErrNotFound) })
}

func hasParentSpan(r *http.Request) bool {
	return trace.SpanContextFromContext(r.Context()).IsValid()
}
