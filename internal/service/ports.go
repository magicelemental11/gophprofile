// Package service holds the avatar use cases and the ports they depend on.
package service

import (
	"context"
	"io"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// AvatarRepository persists avatar metadata and processed message ids.
type AvatarRepository interface {
	Create(ctx context.Context, a domain.Avatar) (domain.Avatar, error)
	Get(ctx context.Context, id string) (domain.Avatar, error)
	GetLatestByUser(ctx context.Context, userID string) (domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	MarkUploaded(ctx context.Context, id, etag string) error
	SetUploadStatus(ctx context.Context, id string, s domain.UploadStatus) error
	SetProcessingStatus(ctx context.Context, id string, s domain.ProcessingStatus) error
	CompleteProcessing(ctx context.Context, id string, keys map[domain.Size]string, messageID string) (bool, error)
	SoftDelete(ctx context.Context, id string) error
	IsProcessed(ctx context.Context, messageID string) (bool, error)
	MarkProcessed(ctx context.Context, messageID string) error
	Ping(ctx context.Context) error
}

// ObjectStorage stores image files by key.
type ObjectStorage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, keys []string) error
	Ping(ctx context.Context) error
}

// EventPublisher sends avatar events to the message broker.
type EventPublisher interface {
	Publish(ctx context.Context, routingKey string, event any) error
	Ping(ctx context.Context) error
}
