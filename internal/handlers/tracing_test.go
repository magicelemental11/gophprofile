package handlers

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/telemetry/telemetrytest"
)

func TestServerSpanNamedByRoute(t *testing.T) {
	spans := telemetrytest.Record(t)
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": {ID: "a1", CreatedAt: created}}}

	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	ended := spans.Ended()
	require.Len(t, ended, 1)
	s := ended[0]
	assert.Equal(t, "GET /api/v1/avatars/{id}/metadata", s.Name())
	assert.Equal(t, trace.SpanKindServer, s.SpanKind())
	assert.Equal(t, "/api/v1/avatars/{id}/metadata", telemetrytest.Attrs(s)["http.route"].AsString())
	assert.NotEmpty(t, telemetrytest.Attrs(s)["http.request_id"].AsString())
	assert.Equal(t, s.SpanContext().TraceID().String(), rec.Header().Get("X-Trace-ID"))
}

func TestServerSpanContinuesIncomingTrace(t *testing.T) {
	spans := telemetrytest.Record(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/nope/metadata", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	rec := serve(&fakeService{}, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	ended := spans.Ended()
	require.Len(t, ended, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ended[0].SpanContext().TraceID().String())
	assert.Equal(t, "00f067aa0ba902b7", ended[0].Parent().SpanID().String())
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", rec.Header().Get("X-Trace-ID"))
}

func TestHealthIsNotTraced(t *testing.T) {
	spans := telemetrytest.Record(t)

	rec := httptest.NewRecorder()
	NewRouter(Deps{Logger: discardLogger()}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, spans.Ended())
	assert.Empty(t, rec.Header().Get("X-Trace-ID"))
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
