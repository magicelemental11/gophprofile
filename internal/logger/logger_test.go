package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func spanContext(t *testing.T) (context.Context, trace.SpanContext) {
	t.Helper()
	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	return trace.ContextWithSpanContext(context.Background(), sc), sc
}

func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	var rec map[string]any
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], &rec))
	return rec
}

func TestNewWritesJSONWithService(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "info", "gophprofile-server")
	require.NoError(t, err)

	l.Info("started", "address", ":8080")

	rec := lastRecord(t, &buf)
	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "started", rec["msg"])
	assert.Equal(t, "gophprofile-server", rec["service"])
	assert.Equal(t, ":8080", rec["address"])
	assert.NotContains(t, rec, "trace_id")
}

func TestNewAddsTraceFromContext(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "info", "svc")
	require.NoError(t, err)
	ctx, sc := spanContext(t)

	l.With("user_id", "u@x").InfoContext(ctx, "upload")

	rec := lastRecord(t, &buf)
	assert.Equal(t, sc.TraceID().String(), rec["trace_id"])
	assert.Equal(t, sc.SpanID().String(), rec["span_id"])
	assert.Equal(t, "u@x", rec["user_id"])
}

func TestNewLevel(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "warn", "svc")
	require.NoError(t, err)
	assert.False(t, l.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, l.Enabled(context.Background(), slog.LevelWarn))

	l, err = New(&buf, "DEBUG", "svc")
	require.NoError(t, err)
	assert.True(t, l.Enabled(context.Background(), slog.LevelDebug))

	_, err = New(&buf, "loud", "svc")
	assert.Error(t, err)
}
