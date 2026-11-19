package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// Stats reports the storage used by uploaded, non-deleted avatars in total and for the topUsers
// users storing the most, their count per processing status and the number of unpublished
// outbox events.
func (r *Repository) Stats(ctx context.Context, topUsers int) (domain.Stats, error) {
	s := domain.Stats{TopStorage: map[string]int64{}, Avatars: map[domain.ProcessingStatus]int64{}}
	err := r.collect(ctx, func(scan func(...any) error) error {
		var user string
		var bytes int64
		if err := scan(&user, &bytes); err != nil {
			return err
		}
		s.TopStorage[user] = bytes
		return nil
	}, `
		SELECT user_id, sum(size_bytes)::bigint FROM avatars
		WHERE deleted_at IS NULL AND upload_status = 'uploaded' GROUP BY user_id
		ORDER BY 2 DESC, user_id LIMIT $1`, topUsers)
	if err != nil {
		return domain.Stats{}, fmt.Errorf("storage usage: %w", err)
	}
	err = r.collect(ctx, func(scan func(...any) error) error {
		var status domain.ProcessingStatus
		var n, bytes int64
		if err := scan(&status, &n, &bytes); err != nil {
			return err
		}
		s.Avatars[status] = n
		s.StorageTotal += bytes
		return nil
	}, `
		SELECT processing_status, count(*), sum(size_bytes)::bigint FROM avatars
		WHERE deleted_at IS NULL AND upload_status = 'uploaded' GROUP BY processing_status`)
	if err != nil {
		return domain.Stats{}, fmt.Errorf("count avatars: %w", err)
	}
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&s.OutboxPending); err != nil {
		return domain.Stats{}, fmt.Errorf("count outbox: %w", err)
	}
	return s, nil
}

func (r *Repository) collect(ctx context.Context, row func(scan func(...any) error) error, sql string, args ...any) error {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := row(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// PoolStat returns a snapshot of the connection pool statistics.
func (r *Repository) PoolStat() *pgxpool.Stat {
	return r.pool.Stat()
}
