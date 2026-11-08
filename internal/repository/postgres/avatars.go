package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

const avatarColumns = `id, user_id, file_name, mime_type, size_bytes, width, height, s3_key,
	thumbnail_s3_keys, etag, upload_status, processing_status, created_at, updated_at`

// Create inserts a new avatar with the id set by the caller and returns the stored row.
func (r *Repository) Create(ctx context.Context, a domain.Avatar) (domain.Avatar, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, width, height, s3_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+avatarColumns,
		a.ID, a.UserID, a.FileName, a.MimeType, a.SizeBytes, a.Width, a.Height, a.S3Key)
	created, err := scanAvatar(row)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("insert avatar: %w", err)
	}
	return created, nil
}

// Get returns a non-deleted avatar by id.
func (r *Repository) Get(ctx context.Context, id string) (domain.Avatar, error) {
	if !validUUID(id) {
		return domain.Avatar{}, domain.ErrNotFound
	}
	row := r.db.QueryRow(ctx, `SELECT `+avatarColumns+` FROM avatars WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanAvatar(row)
}

// GetLatestByUser returns the newest uploaded, non-deleted avatar of the user.
func (r *Repository) GetLatestByUser(ctx context.Context, userID string) (domain.Avatar, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+avatarColumns+` FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL AND upload_status = $2
		ORDER BY created_at DESC LIMIT 1`, userID, domain.UploadUploaded)
	return scanAvatar(row)
}

// ListByUser returns the user's uploaded, non-deleted avatars, newest first.
func (r *Repository) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+avatarColumns+` FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL AND upload_status = $2
		ORDER BY created_at DESC`, userID, domain.UploadUploaded)
	if err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}
	defer rows.Close()
	list := []domain.Avatar{}
	for rows.Next() {
		a, err := scanAvatar(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}
	return list, nil
}

// MarkUploaded records that the original is stored and saves its ETag.
func (r *Repository) MarkUploaded(ctx context.Context, id, etag string) error {
	return r.exec(ctx, `UPDATE avatars SET upload_status = $2, etag = $3, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`,
		id, domain.UploadUploaded, etag)
}

// SetUploadStatus changes the upload status.
func (r *Repository) SetUploadStatus(ctx context.Context, id string, s domain.UploadStatus) error {
	return r.exec(ctx, `UPDATE avatars SET upload_status = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id, s)
}

// SetProcessingStatus changes the processing status.
func (r *Repository) SetProcessingStatus(ctx context.Context, id string, s domain.ProcessingStatus) error {
	return r.exec(ctx, `UPDATE avatars SET processing_status = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id, s)
}

// CompleteProcessing stores thumbnail keys, marks the avatar completed and records the message
// in one transaction. It reports false and leaves the message unrecorded when the avatar was
// deleted meanwhile, so cleanup can be retried.
func (r *Repository) CompleteProcessing(ctx context.Context, id string, keys map[domain.Size]string, messageID string) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE avatars SET thumbnail_s3_keys = $2, processing_status = $3, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id, keys, domain.ProcessingCompleted)
	if err != nil {
		return false, fmt.Errorf("complete processing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO processed_messages (message_id) VALUES ($1) ON CONFLICT DO NOTHING`, messageID); err != nil {
		return false, fmt.Errorf("record message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// SoftDelete marks the avatar deleted.
func (r *Repository) SoftDelete(ctx context.Context, id string) error {
	return r.exec(ctx, `UPDATE avatars SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
}

// IsProcessed reports whether a message with this id was handled before.
func (r *Repository) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	if !validUUID(messageID) {
		return false, nil
	}
	var ok bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM processed_messages WHERE message_id = $1)`, messageID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check message: %w", err)
	}
	return ok, nil
}

// MarkProcessed records a handled message id; repeated calls are no-ops.
func (r *Repository) MarkProcessed(ctx context.Context, messageID string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO processed_messages (message_id) VALUES ($1) ON CONFLICT DO NOTHING`, messageID)
	if err != nil {
		return fmt.Errorf("record message: %w", err)
	}
	return nil
}

func (r *Repository) exec(ctx context.Context, sql string, id string, args ...any) error {
	if !validUUID(id) {
		return domain.ErrNotFound
	}
	tag, err := r.db.Exec(ctx, sql, append([]any{id}, args...)...)
	if err != nil {
		return fmt.Errorf("update avatar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanAvatar(row pgx.Row) (domain.Avatar, error) {
	var a domain.Avatar
	var keys map[domain.Size]string
	err := row.Scan(&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes, &a.Width, &a.Height, &a.S3Key,
		&keys, &a.ETag, &a.UploadStatus, &a.ProcessingStatus, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Avatar{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("scan avatar: %w", err)
	}
	if len(keys) > 0 {
		a.ThumbnailKeys = keys
	}
	return a, nil
}

func validUUID(s string) bool {
	return uuid.Validate(s) == nil
}
