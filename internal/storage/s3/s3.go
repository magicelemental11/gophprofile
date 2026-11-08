// Package s3 stores avatar files in an S3-compatible object storage such as MinIO.
package s3

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/domain"
)

// Storage keeps objects in one bucket.
type Storage struct {
	client *minio.Client
	bucket string
}

// New connects to the storage and creates the bucket when it is missing.
func New(ctx context.Context, cfg config.S3) (*Storage, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
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
	info, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("put %s: %w", key, err)
	}
	return info.ETag, nil
}

// Get opens an object for reading; a missing object is domain.ErrNotFound.
func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
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
		return err
	}
	if !ok {
		return fmt.Errorf("bucket %s does not exist", s.bucket)
	}
	return nil
}
