package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func scrape(t *testing.T, m *Metrics, openMetrics bool) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if openMetrics {
		req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	return string(body)
}

func sampledContext(t *testing.T) context.Context {
	t.Helper()
	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestBusinessMetrics(t *testing.T) {
	m := New()
	ctx := context.Background()

	m.ObserveUpload(ctx, domain.OutcomeSuccess, 200*time.Millisecond)
	m.ObserveUpload(ctx, domain.OutcomeSuccess, time.Second)
	m.ObserveUpload(ctx, domain.OutcomeRejected, time.Millisecond)
	m.ObserveDelete(domain.OutcomeError)

	assert.InDelta(t, 2, testutil.ToFloat64(m.uploads.WithLabelValues(string(domain.OutcomeSuccess))), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.uploads.WithLabelValues(string(domain.OutcomeRejected))), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.deletes.WithLabelValues(string(domain.OutcomeError))), 0)
	out := scrape(t, m, false)
	assert.Contains(t, out, `avatars_upload_duration_seconds_count{status="success"} 2`)
	assert.Contains(t, out, `avatars_upload_duration_seconds_bucket{status="success",le="0.25"} 1`)
}

func TestWorkerMetrics(t *testing.T) {
	m := New()

	m.ObserveMessage(context.Background(), "process", domain.OutcomeSuccess, 300*time.Millisecond)
	m.Retried("delete")
	m.Retried("delete")
	m.DeadLettered("process")

	assert.InDelta(t, 1, testutil.ToFloat64(m.processed.WithLabelValues("process", string(domain.OutcomeSuccess))), 0)
	assert.InDelta(t, 2, testutil.ToFloat64(m.retries.WithLabelValues("delete")), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.deadLettered.WithLabelValues("process")), 0)
	assert.Contains(t, scrape(t, m, false), `avatars_processing_duration_seconds_count{operation="process"} 1`)
}

func TestKnownSeriesStartAtZero(t *testing.T) {
	out := scrape(t, New(), false)

	for _, s := range []string{
		`avatars_uploads_total{status="error"} 0`,
		`avatars_deletes_total{status="success"} 0`,
		`avatars_processing_total{operation="delete",status="error"} 0`,
		`worker_retries_total{operation="process"} 0`,
		`worker_dead_lettered_total{operation="delete"} 0`,
	} {
		assert.Contains(t, out, s)
	}
}

func TestHTTPMetrics(t *testing.T) {
	m := New()

	m.RequestStarted()
	m.RequestStarted()
	m.RequestFinished(context.Background(), http.MethodGet, "/api/v1/avatars/{id}", http.StatusNotFound, 3*time.Millisecond)

	assert.InDelta(t, 1, testutil.ToFloat64(m.httpInFlight), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(m.httpRequests.WithLabelValues(http.MethodGet, "/api/v1/avatars/{id}", "404")), 0)
	assert.Contains(t, scrape(t, m, false), `http_request_duration_seconds_count{method="GET",route="/api/v1/avatars/{id}"} 1`)
}

func TestExemplarsCarryTraceID(t *testing.T) {
	m := New()

	m.ObserveUpload(sampledContext(t), domain.OutcomeSuccess, 100*time.Millisecond)
	m.RequestStarted()
	m.RequestFinished(sampledContext(t), http.MethodPost, "/api/v1/avatars", http.StatusCreated, 100*time.Millisecond)

	out := scrape(t, m, true)
	assert.Contains(t, out, `avatars_upload_duration_seconds_bucket{status="success",le="0.1"} 1 # {trace_id="4bf92f3577b34da6a3ce929d0e0e4736"}`)
	assert.Contains(t, out, `# {trace_id="4bf92f3577b34da6a3ce929d0e0e4736"}`)
	assert.NotContains(t, scrape(t, New(), true), "trace_id")
}

type fakeStats struct {
	stats domain.Stats
	err   error
}

func (f fakeStats) Stats(context.Context) (domain.Stats, error) { return f.stats, f.err }

func TestStatsCollector(t *testing.T) {
	m := New()
	require.NoError(t, m.Register(NewStatsCollector(fakeStats{stats: domain.Stats{
		StorageBytes:  map[string]int64{"a@x": 100, "b@x": 50},
		Avatars:       map[domain.ProcessingStatus]int64{domain.ProcessingCompleted: 2, domain.ProcessingFailed: 1},
		OutboxPending: 3,
	}}, time.Second)))

	out := scrape(t, m, false)
	assert.Contains(t, out, `avatars_storage_bytes{user_id="a@x"} 100`)
	assert.Contains(t, out, `avatars_storage_bytes{user_id="b@x"} 50`)
	assert.Contains(t, out, `avatars_storage_total_bytes 150`)
	assert.Contains(t, out, `avatars_total{processing_status="completed"} 2`)
	assert.Contains(t, out, `avatars_total{processing_status="failed"} 1`)
	assert.Contains(t, out, `outbox_pending_events 3`)
}

func TestStatsCollectorErrorKeepsScrape(t *testing.T) {
	m := New()
	require.NoError(t, m.Register(NewStatsCollector(fakeStats{err: errors.New("db down")}, time.Second)))
	m.ObserveDelete(domain.OutcomeSuccess)

	out := scrape(t, m, false)
	assert.Contains(t, out, `avatars_deletes_total{status="success"} 1`)
	assert.Contains(t, out, "go_goroutines")
	assert.NotContains(t, out, "avatars_storage_total_bytes")
}

type fakePool struct{}

func (fakePool) AcquiredConns() int32                { return 2 }
func (fakePool) IdleConns() int32                    { return 3 }
func (fakePool) TotalConns() int32                   { return 5 }
func (fakePool) MaxConns() int32                     { return 10 }
func (fakePool) AcquireCount() int64                 { return 42 }
func (fakePool) EmptyAcquireCount() int64            { return 7 }
func (fakePool) EmptyAcquireWaitTime() time.Duration { return 1500 * time.Millisecond }

func TestPoolCollector(t *testing.T) {
	m := New()
	require.NoError(t, m.Register(NewPoolCollector(func() PoolStat { return fakePool{} })))

	out := scrape(t, m, false)
	assert.Contains(t, out, `db_pool_connections{state="acquired"} 2`)
	assert.Contains(t, out, `db_pool_connections{state="idle"} 3`)
	assert.Contains(t, out, `db_pool_connections{state="total"} 5`)
	assert.Contains(t, out, `db_pool_max_connections 10`)
	assert.Contains(t, out, `db_pool_acquires_total 42`)
	assert.Contains(t, out, `db_pool_empty_acquires_total 7`)
	assert.Contains(t, out, `db_pool_empty_acquire_wait_seconds_total 1.5`)
}

func TestMetricNamesFollowConventions(t *testing.T) {
	m := New()
	m.ObserveUpload(context.Background(), domain.OutcomeSuccess, time.Millisecond)
	m.ObserveDelete(domain.OutcomeSuccess)
	m.ObserveMessage(context.Background(), "process", domain.OutcomeSuccess, time.Millisecond)
	m.Retried("process")
	m.DeadLettered("process")
	m.RequestStarted()
	m.RequestFinished(context.Background(), http.MethodGet, "/health", http.StatusOK, time.Millisecond)
	require.NoError(t, m.Register(NewPoolCollector(func() PoolStat { return fakePool{} })))
	require.NoError(t, m.Register(NewStatsCollector(fakeStats{}, time.Second)))

	problems, err := testutil.GatherAndLint(m.reg)
	require.NoError(t, err)
	assert.Empty(t, problems)
}

func TestServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "127.0.0.1:0", New().Handler()) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}
