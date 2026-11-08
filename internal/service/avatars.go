package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/imaging"
)

// Image is an avatar file ready to be sent to a client.
type Image struct {
	Body        io.ReadCloser
	ContentType string
	ETag        string
}

// AvatarService implements uploading, reading and deleting avatars.
type AvatarService struct {
	repo    AvatarRepository
	store   ObjectStorage
	pub     EventPublisher
	log     *zap.Logger
	maxSize int64
}

// NewAvatarService wires the service to its ports; maxSize limits an upload in bytes.
func NewAvatarService(repo AvatarRepository, store ObjectStorage, pub EventPublisher, log *zap.Logger, maxSize int64) *AvatarService {
	return &AvatarService{repo: repo, store: store, pub: pub, log: log, maxSize: maxSize}
}

// MaxSize returns the upload limit in bytes.
func (s *AvatarService) MaxSize() int64 {
	return s.maxSize
}

// Upload validates and stores a new avatar, then asks the worker to build thumbnails.
// A failed publish is logged but does not fail the upload: thumbnails are then built on request.
func (s *AvatarService) Upload(ctx context.Context, userID, fileName string, r io.Reader) (domain.Avatar, error) {
	data, err := io.ReadAll(io.LimitReader(r, s.maxSize+1))
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > s.maxSize {
		return domain.Avatar{}, domain.ErrTooLarge
	}
	info, err := imaging.Inspect(data)
	if err != nil {
		return domain.Avatar{}, err
	}

	id := uuid.NewString()
	a, err := s.repo.Create(ctx, domain.Avatar{
		ID:        id,
		UserID:    userID,
		FileName:  fileName,
		MimeType:  info.Format.MIME(),
		SizeBytes: int64(len(data)),
		Width:     info.Width,
		Height:    info.Height,
		S3Key:     domain.OriginalKey(id, info.Format),
	})
	if err != nil {
		return domain.Avatar{}, err
	}

	etag, err := s.store.Put(ctx, a.S3Key, bytes.NewReader(data), int64(len(data)), a.MimeType)
	if err != nil {
		if errStatus := s.repo.SetUploadStatus(context.WithoutCancel(ctx), a.ID, domain.UploadFailed); errStatus != nil {
			s.log.Error("mark upload failed", zap.String("avatar_id", a.ID), zap.Error(errStatus))
		}
		return domain.Avatar{}, err
	}
	if err := s.repo.MarkUploaded(ctx, a.ID, etag); err != nil {
		return domain.Avatar{}, err
	}
	a.UploadStatus, a.ETag = domain.UploadUploaded, etag

	event := domain.AvatarUploadEvent{AvatarID: a.ID, UserID: a.UserID, S3Key: a.S3Key}
	if err := s.pub.Publish(ctx, domain.RoutingUploaded, event); err != nil {
		s.log.Error("publish upload event", zap.String("avatar_id", a.ID), zap.Error(err))
	}
	return a, nil
}

// Get returns avatar metadata.
func (s *AvatarService) Get(ctx context.Context, id string) (domain.Avatar, error) {
	return s.repo.Get(ctx, id)
}

// List returns the user's avatars, newest first.
func (s *AvatarService) List(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return s.repo.ListByUser(ctx, userID)
}

// Open returns the avatar image in the requested size and format; an empty format keeps the stored one.
func (s *AvatarService) Open(ctx context.Context, id string, size domain.Size, format domain.Format) (Image, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		return Image{}, err
	}
	return s.open(ctx, a, size, format)
}

// OpenLatest is Open for the user's current avatar.
func (s *AvatarService) OpenLatest(ctx context.Context, userID string, size domain.Size, format domain.Format) (Image, error) {
	a, err := s.repo.GetLatestByUser(ctx, userID)
	if err != nil {
		return Image{}, err
	}
	return s.open(ctx, a, size, format)
}

func (s *AvatarService) open(ctx context.Context, a domain.Avatar, size domain.Size, format domain.Format) (Image, error) {
	if a.UploadStatus != domain.UploadUploaded {
		return Image{}, domain.ErrNotFound
	}
	original, ok := domain.FormatFromMIME(a.MimeType)
	if !ok {
		return Image{}, fmt.Errorf("avatar %s has unsupported type %q", a.ID, a.MimeType)
	}

	key, srcFormat, resize := a.S3Key, original, size != domain.SizeOriginal
	if thumb, ok := a.ThumbnailKeys[size]; ok && resize {
		key, srcFormat, resize = thumb, domain.FormatJPEG, false
	}
	if format == "" {
		format = srcFormat
		if resize {
			format = domain.FormatJPEG
		}
	}
	etag := `"` + strings.Trim(a.ETag, `"`) + "-" + string(size) + "-" + string(format) + `"`

	body, err := s.store.Get(ctx, key)
	if err != nil {
		return Image{}, err
	}
	if !resize && format == srcFormat {
		return Image{Body: body, ContentType: format.MIME(), ETag: etag}, nil
	}
	defer body.Close()

	img, err := imaging.Decode(body, srcFormat)
	if err != nil {
		return Image{}, fmt.Errorf("decode %s: %w", key, err)
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, imaging.Thumbnail(img, size), format); err != nil {
		return Image{}, fmt.Errorf("encode %s: %w", key, err)
	}
	return Image{Body: io.NopCloser(&buf), ContentType: format.MIME(), ETag: etag}, nil
}

// Delete soft-deletes the avatar if it belongs to the requester and asks the worker to remove its files.
// A failed publish is logged but does not fail the deletion: the files are then left orphaned.
func (s *AvatarService) Delete(ctx context.Context, requesterID, avatarID string) error {
	a, err := s.repo.Get(ctx, avatarID)
	if err != nil {
		return err
	}
	return s.delete(ctx, requesterID, a)
}

// DeleteCurrent is Delete for the user's current avatar.
func (s *AvatarService) DeleteCurrent(ctx context.Context, requesterID, userID string) error {
	if requesterID != userID {
		return domain.ErrForbidden
	}
	a, err := s.repo.GetLatestByUser(ctx, userID)
	if err != nil {
		return err
	}
	return s.delete(ctx, requesterID, a)
}

func (s *AvatarService) delete(ctx context.Context, requesterID string, a domain.Avatar) error {
	if a.UserID != requesterID {
		return domain.ErrForbidden
	}
	if err := s.repo.SoftDelete(ctx, a.ID); err != nil {
		return err
	}
	event := domain.AvatarDeleteEvent{AvatarID: a.ID, S3Keys: a.AllKeys()}
	if err := s.pub.Publish(ctx, domain.RoutingDeleted, event); err != nil {
		s.log.Error("publish delete event", zap.String("avatar_id", a.ID), zap.Error(err))
	}
	return nil
}
