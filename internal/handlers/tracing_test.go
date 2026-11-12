package handlers

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func attr(span sdktrace.ReadOnlySpan, key attribute.Key) attribute.Value {
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

func TestServerSpanNamedByRoute(t *testing.T) {
	spans := recordSpans(t)
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": {ID: "a1", CreatedAt: created}}}

	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	ended := spans.Ended()
	require.Len(t, ended, 1)
	s := ended[0]
	assert.Equal(t, "GET /api/v1/avatars/{id}/metadata", s.Name())
	assert.Equal(t, trace.SpanKindServer, s.SpanKind())
	assert.Equal(t, "/api/v1/avatars/{id}/metadata", attr(s, "http.route").AsString())
	assert.NotEmpty(t, attr(s, "http.request_id").AsString())
	assert.Equal(t, s.SpanContext().TraceID().String(), rec.Header().Get("X-Trace-ID"))
}

func TestServerSpanContinuesIncomingTrace(t *testing.T) {
	spans := recordSpans(t)
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
	spans := recordSpans(t)

	rec := httptest.NewRecorder()
	NewRouter(Deps{Logger: discardLogger()}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, spans.Ended())
	assert.Empty(t, rec.Header().Get("X-Trace-ID"))
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
