package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/imaging"
)

// ErrPermanent marks failures that retrying cannot fix.
var ErrPermanent = errors.New("permanent failure")

// Processor implements the background work: thumbnails and file cleanup.
type Processor struct {
	repo  AvatarRepository
	store ObjectStorage
	log   *zap.Logger
}

// NewProcessor wires the processor to its ports.
func NewProcessor(repo AvatarRepository, store ObjectStorage, log *zap.Logger) *Processor {
	return &Processor{repo: repo, store: store, log: log}
}

// Process generates the requested thumbnails. A repeated message or thumbnails that already
// exist are skipped, and thumbnails of a deleted avatar are removed, so redelivery is safe.
// Errors wrapping ErrPermanent must not be retried.
func (p *Processor) Process(ctx context.Context, messageID string, ev domain.AvatarProcessEvent) error {
	if uuid.Validate(messageID) != nil {
		return fmt.Errorf("%w: invalid message id %q", ErrPermanent, messageID)
	}
	for _, op := range ev.Operations {
		if op.Type != domain.OpResize || !slices.Contains(domain.ThumbnailSizes, op.Size) {
			return fmt.Errorf("%w: unsupported operation %s %s", ErrPermanent, op.Type, op.Size)
		}
	}
	done, err := p.repo.IsProcessed(ctx, messageID)
	if err != nil || done {
		return err
	}
	a, err := p.repo.Get(ctx, ev.AvatarID)
	if errors.Is(err, domain.ErrNotFound) {
		p.log.Info("skip processing of missing avatar", zap.String("avatar_id", ev.AvatarID))
		return p.removeThumbnails(ctx, ev.AvatarID)
	}
	if err != nil {
		return err
	}
	if a.UploadStatus != domain.UploadUploaded {
		return fmt.Errorf("%w: avatar %s is %s", ErrPermanent, a.ID, a.UploadStatus)
	}
	if a.ProcessingStatus == domain.ProcessingCompleted && hasAll(a, ev.Operations) {
		return p.repo.MarkProcessed(ctx, messageID)
	}

	if err := p.repo.SetProcessingStatus(ctx, a.ID, domain.ProcessingProcessing); err != nil {
		return err
	}
	keys, err := p.thumbnails(ctx, a, ev.Operations)
	if err != nil {
		return err
	}
	ok, err := p.repo.CompleteProcessing(ctx, a.ID, keys, messageID)
	if err != nil {
		return err
	}
	if !ok {
		p.log.Info("avatar deleted during processing", zap.String("avatar_id", a.ID))
		return p.removeThumbnails(ctx, a.ID)
	}
	return nil
}

func (p *Processor) removeThumbnails(ctx context.Context, avatarID string) error {
	if err := p.store.Delete(ctx, domain.ThumbnailKeysOf(avatarID)); err != nil {
		return fmt.Errorf("remove thumbnails: %w", err)
	}
	return nil
}

func hasAll(a domain.Avatar, ops []domain.ProcessingOp) bool {
	for _, op := range ops {
		if _, ok := a.ThumbnailKeys[op.Size]; !ok {
			return false
		}
	}
	return true
}

func (p *Processor) thumbnails(ctx context.Context, a domain.Avatar, ops []domain.ProcessingOp) (map[domain.Size]string, error) {
	format, ok := domain.FormatFromMIME(a.MimeType)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported type %q", ErrPermanent, a.MimeType)
	}
	body, err := p.store.Get(ctx, a.S3Key)
	if err != nil {
		return nil, fmt.Errorf("download original: %w", err)
	}
	img, err := imaging.Decode(body, format)
	_ = body.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPermanent, err)
	}

	keys := make(map[domain.Size]string, len(a.ThumbnailKeys)+len(ops))
	for s, k := range a.ThumbnailKeys {
		keys[s] = k
	}
	for _, op := range ops {
		var buf bytes.Buffer
		if err := imaging.Encode(&buf, imaging.Thumbnail(img, op.Size), domain.FormatJPEG); err != nil {
			return nil, fmt.Errorf("%w: encode thumbnail: %w", ErrPermanent, err)
		}
		key := domain.ThumbnailKey(a.ID, op.Size)
		if _, err := p.store.Put(ctx, key, &buf, int64(buf.Len()), domain.FormatJPEG.MIME()); err != nil {
			return nil, fmt.Errorf("upload thumbnail: %w", err)
		}
		keys[op.Size] = key
	}
	return keys, nil
}

// DeleteFiles removes the objects of a deleted avatar. A repeated message is skipped.
func (p *Processor) DeleteFiles(ctx context.Context, messageID string, ev domain.AvatarDeleteEvent) error {
	if uuid.Validate(messageID) != nil {
		return fmt.Errorf("%w: invalid message id %q", ErrPermanent, messageID)
	}
	done, err := p.repo.IsProcessed(ctx, messageID)
	if err != nil || done {
		return err
	}
	if len(ev.S3Keys) > 0 {
		if err := p.store.Delete(ctx, ev.S3Keys); err != nil {
			return fmt.Errorf("delete files: %w", err)
		}
	}
	return p.repo.MarkProcessed(ctx, messageID)
}

// MarkFailed records that processing gave up; a missing avatar is not an error.
func (p *Processor) MarkFailed(ctx context.Context, avatarID string) error {
	err := p.repo.SetProcessingStatus(ctx, avatarID, domain.ProcessingFailed)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return err
}
