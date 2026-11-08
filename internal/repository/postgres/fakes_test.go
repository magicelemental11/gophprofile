package postgres

import (
	"context"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type call struct {
	sql  string
	args []any
}

type result struct {
	rows     [][]any
	affected int64
	err      error
	rowErr   error
}

type fakeDB struct {
	t         *testing.T
	calls     []call
	results   []result
	beginErr  error
	commitErr error
	committed bool
	rolled    bool
}

func (f *fakeDB) expect(r result) { f.results = append(f.results, r) }

func (f *fakeDB) next(sql string, args []any) result {
	f.calls = append(f.calls, call{sql: sql, args: args})
	if len(f.results) == 0 {
		f.t.Fatalf("unexpected query: %s", sql)
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r
}

func (f *fakeDB) lastSQL(t *testing.T, pattern string) {
	t.Helper()
	if len(f.calls) == 0 || !regexp.MustCompile(pattern).MatchString(f.calls[len(f.calls)-1].sql) {
		t.Fatalf("last query does not match %q: %+v", pattern, f.calls)
	}
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r := f.next(sql, args)
	return pgconn.NewCommandTag("UPDATE " + strconv.FormatInt(r.affected, 10)), r.err
}

func (f *fakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	r := f.next(sql, args)
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{rows: r.rows, err: r.rowErr, pos: -1}, nil
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	r := f.next(sql, args)
	if r.err != nil {
		return fakeRow{err: r.err}
	}
	if len(r.rows) == 0 {
		return fakeRow{err: pgx.ErrNoRows}
	}
	return fakeRow{values: r.rows[0]}
}

func (f *fakeDB) Begin(context.Context) (pgx.Tx, error) {
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return &fakeTx{db: f}, nil
}

type fakeTx struct {
	pgx.Tx
	db *fakeDB
}

func (tx *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return tx.db.Exec(ctx, sql, args...)
}

func (tx *fakeTx) Commit(context.Context) error {
	if tx.db.commitErr != nil {
		return tx.db.commitErr
	}
	tx.db.committed = true
	return nil
}

func (tx *fakeTx) Rollback(context.Context) error {
	if !tx.db.committed {
		tx.db.rolled = true
	}
	return nil
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assign(dest, r.values)
}

type fakeRows struct {
	pgx.Rows
	rows [][]any
	pos  int
	err  error
}

func (r *fakeRows) Next() bool {
	if r.err != nil {
		return false
	}
	r.pos++
	return r.pos < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error { return assign(dest, r.rows[r.pos]) }
func (r *fakeRows) Err() error             { return r.err }
func (r *fakeRows) Close()                 {}

func assign(dest, values []any) error {
	for i, d := range dest {
		v := reflect.ValueOf(values[i])
		target := reflect.ValueOf(d).Elem()
		if !v.IsValid() {
			target.SetZero()
			continue
		}
		target.Set(v.Convert(target.Type()))
	}
	return nil
}
