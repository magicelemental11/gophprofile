package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/imaging"
)

// Image is an avatar variant ready to be sent to a client. ContentType and ETag come from
// metadata, so a client cache can be validated before Open reads the file.
type Image struct {
	ContentType string
	ETag        string
	Open        func(ctx context.Context) (io.ReadCloser, error)
}

// AvatarService implements uploading, reading and deleting avatars.
type AvatarService struct {
	repo    AvatarRepository
	store   ObjectStorage
	log     *zap.Logger
	maxSize int64
}

// NewAvatarService wires the service to its ports; maxSize limits an upload in bytes.
func NewAvatarService(repo AvatarRepository, store ObjectStorage, log *zap.Logger, maxSize int64) *AvatarService {
	return &AvatarService{repo: repo, store: store, log: log, maxSize: maxSize}
}

// MaxSize returns the upload limit in bytes.
func (s *AvatarService) MaxSize() int64 {
	return s.maxSize
}

// Upload validates and stores a new avatar. The event asking the worker to build thumbnails is
// queued in the outbox together with the uploaded status, so it cannot be lost. If the upload
// cannot be completed, the stored original is removed and the avatar is marked failed.
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
	key := domain.OriginalKey(id, info.Format)
	ev, err := newEvent(domain.RoutingUploaded, domain.AvatarUploadEvent{AvatarID: id, UserID: userID, S3Key: key})
	if err != nil {
		return domain.Avatar{}, err
	}
	a, err := s.repo.Create(ctx, domain.Avatar{
		ID:        id,
		UserID:    userID,
		FileName:  domain.CleanFileName(fileName),
		MimeType:  info.Format.MIME(),
		SizeBytes: int64(len(data)),
		Width:     info.Width,
		Height:    info.Height,
		S3Key:     key,
	})
	if err != nil {
		return domain.Avatar{}, err
	}

	etag, err := s.store.Put(ctx, a.S3Key, bytes.NewReader(data), int64(len(data)), a.MimeType)
	if err != nil {
		s.markUploadFailed(ctx, a.ID)
		return domain.Avatar{}, err
	}
	if err := s.repo.MarkUploaded(ctx, a.ID, etag, ev); err != nil {
		s.discardOriginal(ctx, a)
		if !errors.Is(err, domain.ErrNotFound) {
			s.markUploadFailed(ctx, a.ID)
		}
		return domain.Avatar{}, err
	}
	a.UploadStatus, a.ETag = domain.UploadUploaded, etag
	return a, nil
}

func (s *AvatarService) markUploadFailed(ctx context.Context, id string) {
	if err := s.repo.SetUploadStatus(context.WithoutCancel(ctx), id, domain.UploadFailed); err != nil {
		s.log.Error("mark upload failed", zap.String("avatar_id", id), zap.Error(err))
	}
}

func (s *AvatarService) discardOriginal(ctx context.Context, a domain.Avatar) {
	if err := s.store.Delete(context.WithoutCancel(ctx), []string{a.S3Key}); err != nil {
		s.log.Error("remove original of unfinished upload", zap.String("avatar_id", a.ID), zap.Error(err))
	}
}

func newEvent(routingKey string, payload any) (domain.Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s event: %w", routingKey, err)
	}
	return domain.Event{ID: uuid.NewString(), RoutingKey: routingKey, Payload: body}, nil
}

// Get returns the metadata of a non-deleted avatar.
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
	return s.open(a, size, format)
}

// OpenLatest is Open for the user's current avatar.
func (s *AvatarService) OpenLatest(ctx context.Context, userID string, size domain.Size, format domain.Format) (Image, error) {
	a, err := s.repo.GetLatestByUser(ctx, userID)
	if err != nil {
		return Image{}, err
	}
	return s.open(a, size, format)
}

func (s *AvatarService) open(a domain.Avatar, size domain.Size, format domain.Format) (Image, error) {
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
	v := variant{key: key, src: srcFormat, size: size, format: format, resize: resize}
	return Image{
		ContentType: format.MIME(),
		ETag:        etag,
		Open:        func(ctx context.Context) (io.ReadCloser, error) { return s.read(ctx, v) },
	}, nil
}

type variant struct {
	key    string
	src    domain.Format
	size   domain.Size
	format domain.Format
	resize bool
}

func (s *AvatarService) read(ctx context.Context, v variant) (io.ReadCloser, error) {
	body, err := s.store.Get(ctx, v.key)
	if err != nil {
		return nil, err
	}
	if !v.resize && v.format == v.src {
		return body, nil
	}
	defer body.Close()

	img, err := imaging.Decode(body, v.src)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", v.key, err)
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, imaging.Thumbnail(img, v.size), v.format); err != nil {
		return nil, fmt.Errorf("encode %s: %w", v.key, err)
	}
	return io.NopCloser(&buf), nil
}

// Delete soft-deletes the avatar if it belongs to the requester and, in the same transaction,
// queues the event asking the worker to remove its files.
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
	ev, err := newEvent(domain.RoutingDeleted, domain.AvatarDeleteEvent{AvatarID: a.ID, S3Keys: a.AllKeys()})
	if err != nil {
		return err
	}
	return s.repo.SoftDelete(ctx, a.ID, ev)
}
