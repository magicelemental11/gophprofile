package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/service"
)

var errTemporary = errors.New("s3 unavailable")

type fakeProcessor struct {
	errs    []error
	calls   int
	got     []domain.AvatarProcessEvent
	deleted []domain.AvatarDeleteEvent
	failed  []string
	msgIDs  []string
	failErr error
}

func (f *fakeProcessor) Process(_ context.Context, messageID string, ev domain.AvatarProcessEvent) error {
	f.calls++
	f.got = append(f.got, ev)
	f.msgIDs = append(f.msgIDs, messageID)
	return f.next()
}

func (f *fakeProcessor) DeleteFiles(_ context.Context, messageID string, ev domain.AvatarDeleteEvent) error {
	f.calls++
	f.deleted = append(f.deleted, ev)
	f.msgIDs = append(f.msgIDs, messageID)
	return f.next()
}

func (f *fakeProcessor) next() error {
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakeProcessor) MarkFailed(_ context.Context, avatarID string) error {
	f.failed = append(f.failed, avatarID)
	return f.failErr
}

type recordedSleeps struct {
	delays []time.Duration
}

func (r *recordedSleeps) sleep(ctx context.Context, d time.Duration) error {
	r.delays = append(r.delays, d)
	return ctx.Err()
}

func newTestWorker(p *fakeProcessor, maxRetries int) (*Worker, *recordedSleeps) {
	w := New(p, zap.NewNop(), maxRetries)
	s := &recordedSleeps{}
	w.sleep = s.sleep
	return w, s
}

func body(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestHandleUploaded(t *testing.T) {
	p := &fakeProcessor{}
	w, sleeps := newTestWorker(p, 5)

	err := w.Handle(context.Background(), Message{
		ID: "m1", RoutingKey: domain.RoutingUploaded,
		Body: body(t, domain.AvatarUploadEvent{AvatarID: "a1", UserID: "u", S3Key: "k"}),
	})
	require.NoError(t, err)
	require.Len(t, p.got, 1)
	assert.Equal(t, domain.AvatarProcessEvent{AvatarID: "a1", Operations: domain.DefaultOperations()}, p.got[0])
	assert.Equal(t, []string{"m1"}, p.msgIDs)
	assert.Empty(t, sleeps.delays)
}

func TestHandleProcess(t *testing.T) {
	p := &fakeProcessor{}
	w, _ := newTestWorker(p, 5)
	ev := domain.AvatarProcessEvent{AvatarID: "a1", Operations: []domain.ProcessingOp{{Type: domain.OpResize, Size: domain.Size300}}}

	require.NoError(t, w.Handle(context.Background(), Message{ID: "m", RoutingKey: domain.RoutingProcess, Body: body(t, ev)}))
	assert.Equal(t, []domain.AvatarProcessEvent{ev}, p.got)
}

func TestHandleDeleted(t *testing.T) {
	p := &fakeProcessor{errs: []error{errTemporary, nil}}
	w, sleeps := newTestWorker(p, 5)
	ev := domain.AvatarDeleteEvent{AvatarID: "a1", S3Keys: []string{"originals/a1.png", "thumbnails/a1/100x100.jpg"}}

	require.NoError(t, w.Handle(context.Background(), Message{ID: "m", RoutingKey: domain.RoutingDeleted, Body: body(t, ev)}))
	assert.Equal(t, []domain.AvatarDeleteEvent{ev, ev}, p.deleted)
	assert.Equal(t, []string{"m", "m"}, p.msgIDs)
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, sleeps.delays)
	assert.Empty(t, p.got)
}

func TestHandleDeletedGivesUpWithoutMarkFailed(t *testing.T) {
	p := &fakeProcessor{errs: []error{errTemporary, errTemporary}}
	w, _ := newTestWorker(p, 2)

	err := w.Handle(context.Background(), Message{ID: "m", RoutingKey: domain.RoutingDeleted, Body: body(t, domain.AvatarDeleteEvent{AvatarID: "a1"})})
	require.ErrorIs(t, err, errTemporary)
	assert.Equal(t, 2, p.calls)
	assert.Empty(t, p.failed)
}

func TestHandleRetriesWithBackoff(t *testing.T) {
	p := &fakeProcessor{errs: []error{errTemporary, errTemporary, nil}}
	w, sleeps := newTestWorker(p, 5)

	err := w.Handle(context.Background(), Message{ID: "m", RoutingKey: domain.RoutingUploaded, Body: body(t, domain.AvatarUploadEvent{AvatarID: "a1"})})
	require.NoError(t, err)
	assert.Equal(t, 3, p.calls)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second}, sleeps.delays)
	assert.Empty(t, p.failed)
}

func TestHandleGivesUp(t *testing.T) {
	p := &fakeProcessor{errs: []error{errTemporary, errTemporary, errTemporary, errTemporary}}
	w, sleeps := newTestWorker(p, 4)

	err := w.Handle(context.Background(), Message{ID: "m", RoutingKey: domain.RoutingUploaded, Body: body(t, domain.AvatarUploadEvent{AvatarID: "a1"})})
	require.ErrorIs(t, err, errTemporary)
	assert.Equal(t, 4, p.calls)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}, sleeps.delays)
	assert.Equal(t, []string{"a1"}, p.failed)
}

func TestHandlePermanentNotRetried(t *testing.T) {
	p := &fakeProcessor{errs: []error{fmt.Errorf("%w: corrupted", service.ErrPermanent)}}
	w, sleeps := newTestWorker(p, 5)

	err := w.Handle(context.Background(), Message{ID: "m", RoutingKey: domain.RoutingUploaded, Body: body(t, domain.AvatarUploadEvent{AvatarID: "a1"})})
	require.ErrorIs(t, err, service.ErrPermanent)
	assert.Equal(t, 1, p.calls)
	assert.Empty(t, sleeps.delays)
	assert.Equal(t, []string{"a1"}, p.failed)
}

func TestHandleBadMessages(t *testing.T) {
	p := &fakeProcessor{}
	w, _ := newTestWorker(p, 5)

	assert.ErrorIs(t, w.Handle(context.Background(), Message{RoutingKey: domain.RoutingUploaded, Body: []byte("{")}), service.ErrPermanent)
	assert.ErrorIs(t, w.Handle(context.Background(), Message{RoutingKey: domain.RoutingProcess, Body: []byte("[]")}), service.ErrPermanent)
	assert.ErrorIs(t, w.Handle(context.Background(), Message{RoutingKey: domain.RoutingDeleted, Body: []byte("{")}), service.ErrPermanent)
	assert.ErrorIs(t, w.Handle(context.Background(), Message{RoutingKey: "avatar.unknown", Body: []byte("{}")}), service.ErrPermanent)
	assert.Zero(t, p.calls)
}

func TestHandleStopsOnCancel(t *testing.T) {
	p := &fakeProcessor{errs: []error{errTemporary, errTemporary}}
	w, _ := newTestWorker(p, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.Handle(ctx, Message{ID: "m", RoutingKey: domain.RoutingUploaded, Body: body(t, domain.AvatarUploadEvent{AvatarID: "a1"})})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, p.calls)
}

func TestBackoffCap(t *testing.T) {
	assert.Equal(t, 500*time.Millisecond, backoff(0))
	assert.Equal(t, 4*time.Second, backoff(3))
	assert.Equal(t, maxDelay, backoff(20))
}

func TestSleepContext(t *testing.T) {
	assert.NoError(t, sleepContext(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, sleepContext(ctx, time.Hour), context.Canceled)
}
