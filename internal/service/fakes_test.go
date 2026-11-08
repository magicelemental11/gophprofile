package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

var errBoom = errors.New("boom")

type fakeRepo struct {
	mu        sync.Mutex
	avatars   map[string]domain.Avatar
	deleted   map[string]bool
	processed map[string]bool
	clock     time.Time
	failNext  error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		avatars:   map[string]domain.Avatar{},
		deleted:   map[string]bool{},
		processed: map[string]bool{},
		clock:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (r *fakeRepo) fail() error {
	err := r.failNext
	r.failNext = nil
	return err
}

func (r *fakeRepo) Create(_ context.Context, a domain.Avatar) (domain.Avatar, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return domain.Avatar{}, err
	}
	r.clock = r.clock.Add(time.Second)
	a.UploadStatus = domain.UploadUploading
	a.ProcessingStatus = domain.ProcessingPending
	a.CreatedAt, a.UpdatedAt = r.clock, r.clock
	r.avatars[a.ID] = a
	return a, nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (domain.Avatar, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return domain.Avatar{}, err
	}
	a, ok := r.avatars[id]
	if !ok || r.deleted[id] {
		return domain.Avatar{}, domain.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) GetLatestByUser(ctx context.Context, userID string) (domain.Avatar, error) {
	list, err := r.ListByUser(ctx, userID)
	if err != nil {
		return domain.Avatar{}, err
	}
	for _, a := range list {
		if a.UploadStatus == domain.UploadUploaded {
			return a, nil
		}
	}
	return domain.Avatar{}, domain.ErrNotFound
}

func (r *fakeRepo) ListByUser(_ context.Context, userID string) ([]domain.Avatar, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return nil, err
	}
	list := []domain.Avatar{}
	for id, a := range r.avatars {
		if a.UserID == userID && !r.deleted[id] {
			list = append(list, a)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	return list, nil
}

func (r *fakeRepo) update(id string, fn func(*domain.Avatar)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return err
	}
	a, ok := r.avatars[id]
	if !ok || r.deleted[id] {
		return domain.ErrNotFound
	}
	fn(&a)
	r.clock = r.clock.Add(time.Second)
	a.UpdatedAt = r.clock
	r.avatars[id] = a
	return nil
}

func (r *fakeRepo) MarkUploaded(_ context.Context, id, etag string) error {
	return r.update(id, func(a *domain.Avatar) { a.UploadStatus, a.ETag = domain.UploadUploaded, etag })
}

func (r *fakeRepo) SetUploadStatus(_ context.Context, id string, s domain.UploadStatus) error {
	return r.update(id, func(a *domain.Avatar) { a.UploadStatus = s })
}

func (r *fakeRepo) SetProcessingStatus(_ context.Context, id string, s domain.ProcessingStatus) error {
	return r.update(id, func(a *domain.Avatar) { a.ProcessingStatus = s })
}

func (r *fakeRepo) CompleteProcessing(_ context.Context, id string, keys map[domain.Size]string, messageID string) (bool, error) {
	err := r.update(id, func(a *domain.Avatar) { a.ProcessingStatus, a.ThumbnailKeys = domain.ProcessingCompleted, keys })
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	r.processed[messageID] = true
	r.mu.Unlock()
	return true, nil
}

func (r *fakeRepo) SoftDelete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return err
	}
	if _, ok := r.avatars[id]; !ok || r.deleted[id] {
		return domain.ErrNotFound
	}
	r.deleted[id] = true
	return nil
}

func (r *fakeRepo) IsProcessed(_ context.Context, messageID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return false, err
	}
	return r.processed[messageID], nil
}

func (r *fakeRepo) MarkProcessed(_ context.Context, messageID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail(); err != nil {
		return err
	}
	r.processed[messageID] = true
	return nil
}

func (r *fakeRepo) Ping(context.Context) error { return nil }

type fakeStore struct {
	mu       sync.Mutex
	objects  map[string][]byte
	types    map[string]string
	putFails int
	getErr   error
	delErr   error
	puts     int
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}, types: map[string]string{}}
}

func (s *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putFails > 0 {
		s.putFails--
		return "", errBoom
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s.puts++
	s.objects[key] = data
	s.types[key] = contentType
	return `"etag-` + key + `"`, nil
}

func (s *fakeStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeStore) Delete(_ context.Context, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delErr != nil {
		return s.delErr
	}
	for _, k := range keys {
		delete(s.objects, k)
	}
	return nil
}

func (s *fakeStore) Ping(context.Context) error { return nil }

func (s *fakeStore) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

type published struct {
	key   string
	event any
}

type fakePublisher struct {
	mu     sync.Mutex
	events []published
	err    error
}

func (p *fakePublisher) Publish(ctx context.Context, key string, event any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.events = append(p.events, published{key, event})
	return nil
}

func (p *fakePublisher) Ping(context.Context) error { return nil }

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
