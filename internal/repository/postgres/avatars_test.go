package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

const (
	avatarID  = "6f1c2a4e-8b1d-4c3e-9f7a-2b5d8e0c1a34"
	messageID = "0b8e6d2c-1f4a-4e7b-a3c9-5d2f8e1b7c60"
)

var (
	errDB = errors.New("connection reset")
	ts    = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
)

func newFakeRepo(t *testing.T) (*Repository, *fakeDB) {
	t.Helper()
	db := &fakeDB{t: t}
	t.Cleanup(func() { assert.Empty(t, db.results, "unused results") })
	return &Repository{db: db}, db
}

func avatarRow(id string, keys map[domain.Size]string) []any {
	return []any{id, "u", "cat.png", "image/png", int64(10), 4, 3, "avatars/" + id + "/original.png",
		keys, `"e"`, domain.UploadUploaded, domain.ProcessingCompleted, ts, ts}
}

func TestGet(t *testing.T) {
	r, db := newFakeRepo(t)
	keys := map[domain.Size]string{domain.Size100: "k1"}
	db.expect(result{rows: [][]any{avatarRow(avatarID, keys)}})

	a, err := r.Get(context.Background(), avatarID)
	require.NoError(t, err)
	db.lastSQL(t, `FROM avatars WHERE id = \$1 AND deleted_at IS NULL`)
	assert.Equal(t, []any{avatarID}, db.calls[0].args)
	assert.Equal(t, domain.Avatar{
		ID: avatarID, UserID: "u", FileName: "cat.png", MimeType: "image/png", SizeBytes: 10, Width: 4, Height: 3,
		S3Key: "avatars/" + avatarID + "/original.png", ThumbnailKeys: keys, ETag: `"e"`,
		UploadStatus: domain.UploadUploaded, ProcessingStatus: domain.ProcessingCompleted, CreatedAt: ts, UpdatedAt: ts,
	}, a)
}

func TestGetErrors(t *testing.T) {
	r, db := newFakeRepo(t)

	_, err := r.Get(context.Background(), "not-a-uuid")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.Empty(t, db.calls)

	db.expect(result{})
	_, err = r.Get(context.Background(), avatarID)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	db.expect(result{err: errDB})
	_, err = r.Get(context.Background(), avatarID)
	assert.ErrorIs(t, err, errDB)
	assert.NotErrorIs(t, err, domain.ErrNotFound)
}

func TestGetLatestByUser(t *testing.T) {
	r, db := newFakeRepo(t)
	db.expect(result{rows: [][]any{avatarRow(avatarID, nil)}})

	a, err := r.GetLatestByUser(context.Background(), "u")
	require.NoError(t, err)
	db.lastSQL(t, `WHERE user_id = \$1 AND deleted_at IS NULL AND upload_status = \$2`)
	assert.Equal(t, []any{"u", domain.UploadUploaded}, db.calls[0].args)
	assert.Equal(t, avatarID, a.ID)
	assert.Nil(t, a.ThumbnailKeys)
}

func TestListByUser(t *testing.T) {
	r, db := newFakeRepo(t)
	other := "1d3b5f7a-9c2e-4b6d-8f0a-3e5c7b9d1f28"
	db.expect(result{rows: [][]any{avatarRow(avatarID, nil), avatarRow(other, nil)}})

	list, err := r.ListByUser(context.Background(), "u")
	require.NoError(t, err)
	db.lastSQL(t, `ORDER BY created_at DESC`)
	db.lastSQL(t, `upload_status = \$2`)
	require.Len(t, list, 2)
	assert.Equal(t, other, list[1].ID)

	db.expect(result{})
	list, err = r.ListByUser(context.Background(), "nobody")
	require.NoError(t, err)
	assert.NotNil(t, list)
	assert.Empty(t, list)
}

func TestListByUserErrors(t *testing.T) {
	r, db := newFakeRepo(t)

	db.expect(result{err: errDB})
	_, err := r.ListByUser(context.Background(), "u")
	assert.ErrorIs(t, err, errDB)

	db.expect(result{rows: [][]any{avatarRow(avatarID, nil)}, rowErr: errDB})
	_, err = r.ListByUser(context.Background(), "u")
	assert.ErrorIs(t, err, errDB)
}

func TestCreate(t *testing.T) {
	r, db := newFakeRepo(t)
	in := domain.Avatar{ID: avatarID, UserID: "u", FileName: "cat.png", MimeType: "image/png", SizeBytes: 10, Width: 4, Height: 3, S3Key: "k"}
	db.expect(result{rows: [][]any{avatarRow(avatarID, nil)}})

	a, err := r.Create(context.Background(), in)
	require.NoError(t, err)
	db.lastSQL(t, `INSERT INTO avatars`)
	assert.Equal(t, []any{avatarID, "u", "cat.png", "image/png", int64(10), 4, 3, "k"}, db.calls[0].args)
	assert.Equal(t, ts, a.CreatedAt)

	db.expect(result{err: errDB})
	_, err = r.Create(context.Background(), in)
	assert.ErrorIs(t, err, errDB)
}

func TestUpdates(t *testing.T) {
	ctx := context.Background()
	calls := map[string]struct {
		do  func(r *Repository, id string) error
		sql string
	}{
		"SetUploadStatus": {func(r *Repository, id string) error { return r.SetUploadStatus(ctx, id, domain.UploadFailed) }, `SET upload_status = \$2,`},
		"SetProcessingStatus": {func(r *Repository, id string) error {
			return r.SetProcessingStatus(ctx, id, domain.ProcessingProcessing)
		}, `SET processing_status = \$2`},
	}
	for name, c := range calls {
		t.Run(name, func(t *testing.T) {
			r, db := newFakeRepo(t)

			db.expect(result{affected: 1})
			assert.NoError(t, c.do(r, avatarID))
			db.lastSQL(t, c.sql+`.* WHERE id = \$1 AND deleted_at IS NULL`)
			assert.Equal(t, avatarID, db.calls[0].args[0])

			db.expect(result{affected: 0})
			assert.ErrorIs(t, c.do(r, avatarID), domain.ErrNotFound)

			db.expect(result{err: errDB})
			err := c.do(r, avatarID)
			assert.ErrorIs(t, err, errDB)
			assert.NotErrorIs(t, err, domain.ErrNotFound)

			assert.ErrorIs(t, c.do(r, "bad-id"), domain.ErrNotFound)
			assert.Len(t, db.calls, 3)
		})
	}
}

func TestUpdatesWithEvent(t *testing.T) {
	ctx := context.Background()
	ev := domain.Event{ID: messageID, RoutingKey: "avatar.test", Payload: []byte(`{"a":1}`)}
	calls := map[string]struct {
		do  func(r *Repository, id string) error
		sql string
	}{
		"MarkUploaded": {func(r *Repository, id string) error { return r.MarkUploaded(ctx, id, `"e"`, ev) }, `SET upload_status = \$2, etag = \$3`},
		"SoftDelete":   {func(r *Repository, id string) error { return r.SoftDelete(ctx, id, ev) }, `SET deleted_at = now\(\)`},
	}
	for name, c := range calls {
		t.Run(name, func(t *testing.T) {
			t.Run("queues event", func(t *testing.T) {
				r, db := newFakeRepo(t)
				db.expect(result{affected: 1})
				db.expect(result{affected: 1})

				require.NoError(t, c.do(r, avatarID))
				assert.Regexp(t, c.sql+`.* WHERE id = \$1 AND deleted_at IS NULL`, db.calls[0].sql)
				assert.Equal(t, avatarID, db.calls[0].args[0])
				db.lastSQL(t, `INSERT INTO outbox`)
				assert.Equal(t, []any{ev.ID, ev.RoutingKey, ev.Payload}, db.calls[1].args)
				assert.True(t, db.committed)
			})

			t.Run("not found", func(t *testing.T) {
				r, db := newFakeRepo(t)
				db.expect(result{affected: 0})

				assert.ErrorIs(t, c.do(r, avatarID), domain.ErrNotFound)
				assert.Len(t, db.calls, 1, "no event for a missing avatar")
				assert.True(t, db.rolled)

				assert.ErrorIs(t, c.do(r, "bad-id"), domain.ErrNotFound)
				assert.Len(t, db.calls, 1)
			})

			for _, step := range []string{"update", "queue"} {
				t.Run(step+" fails", func(t *testing.T) {
					r, db := newFakeRepo(t)
					if step == "queue" {
						db.expect(result{affected: 1})
					}
					db.expect(result{err: errDB})

					err := c.do(r, avatarID)
					assert.ErrorIs(t, err, errDB)
					assert.NotErrorIs(t, err, domain.ErrNotFound)
					assert.True(t, db.rolled)
				})
			}

			t.Run("begin fails", func(t *testing.T) {
				r, db := newFakeRepo(t)
				db.beginErr = errDB
				assert.ErrorIs(t, c.do(r, avatarID), errDB)
			})

			t.Run("commit fails", func(t *testing.T) {
				r, db := newFakeRepo(t)
				db.expect(result{affected: 1})
				db.expect(result{affected: 1})
				db.commitErr = errDB
				assert.ErrorIs(t, c.do(r, avatarID), errDB)
			})
		})
	}
}

func TestCompleteProcessingTx(t *testing.T) {
	keys := map[domain.Size]string{domain.Size100: "k1", domain.Size300: "k3"}
	ctx := context.Background()

	t.Run("completed", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{affected: 1})
		db.expect(result{affected: 1})

		ok, err := r.CompleteProcessing(ctx, avatarID, keys, messageID)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, db.committed)
		assert.Equal(t, []any{avatarID, keys, domain.ProcessingCompleted}, db.calls[0].args)
		db.lastSQL(t, `INSERT INTO processed_messages`)
		assert.Equal(t, []any{messageID}, db.calls[1].args)
	})

	t.Run("deleted meanwhile", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{affected: 0})

		ok, err := r.CompleteProcessing(ctx, avatarID, keys, messageID)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Len(t, db.calls, 1)
		assert.False(t, db.committed)
		assert.True(t, db.rolled)
	})

	t.Run("update fails", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{err: errDB})

		_, err := r.CompleteProcessing(ctx, avatarID, keys, messageID)
		assert.ErrorIs(t, err, errDB)
		assert.True(t, db.rolled)
	})

	t.Run("record fails", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{affected: 1})
		db.expect(result{err: errDB})

		_, err := r.CompleteProcessing(ctx, avatarID, keys, messageID)
		assert.ErrorIs(t, err, errDB)
		assert.True(t, db.rolled)
	})

	t.Run("begin fails", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.beginErr = errDB

		_, err := r.CompleteProcessing(ctx, avatarID, keys, messageID)
		assert.ErrorIs(t, err, errDB)
	})

	t.Run("commit fails", func(t *testing.T) {
		r, db := newFakeRepo(t)
		db.expect(result{affected: 1})
		db.expect(result{affected: 1})
		db.commitErr = errDB

		_, err := r.CompleteProcessing(ctx, avatarID, keys, messageID)
		assert.ErrorIs(t, err, errDB)
		assert.True(t, db.rolled)
	})
}

func TestProcessedMessagesQueries(t *testing.T) {
	r, db := newFakeRepo(t)
	ctx := context.Background()

	ok, err := r.IsProcessed(ctx, "not-a-uuid")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, db.calls)

	db.expect(result{rows: [][]any{{true}}})
	ok, err = r.IsProcessed(ctx, messageID)
	require.NoError(t, err)
	assert.True(t, ok)
	db.lastSQL(t, `SELECT EXISTS`)

	db.expect(result{err: errDB})
	_, err = r.IsProcessed(ctx, messageID)
	assert.ErrorIs(t, err, errDB)

	db.expect(result{affected: 0})
	assert.NoError(t, r.MarkProcessed(ctx, messageID))
	db.lastSQL(t, `INSERT INTO processed_messages .* ON CONFLICT DO NOTHING`)

	db.expect(result{err: errDB})
	assert.ErrorIs(t, r.MarkProcessed(ctx, messageID), errDB)
}
