package telemetry

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

type keepSpans struct{ *tracetest.InMemoryExporter }

func (keepSpans) Shutdown(context.Context) error { return nil }

func TestSetupExportsSpansWithServiceName(t *testing.T) {
	exp := keepSpans{tracetest.NewInMemoryExporter()}
	shutdown := setup(Config{ServiceName: "gophprofile-test", Version: "1.2.3", SampleRatio: 1}, exp)

	_, span := otel.Tracer("test").Start(context.Background(), "op")
	span.End()
	require.NoError(t, shutdown(context.Background()))

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "op", spans[0].Name)
	attrs := spans[0].Resource.Attributes()
	assert.Contains(t, attrs, semconv.ServiceName("gophprofile-test"))
	assert.Contains(t, attrs, semconv.ServiceVersion("1.2.3"))
}

func TestSetupWithoutExporterPropagates(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{ServiceName: "svc"})
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))

	carrier := propagation.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	_, span := otel.Tracer("test").Start(ctx, "op")
	defer span.End()
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", span.SpanContext().TraceID().String())
	assert.False(t, span.IsRecording())
}

var errClient = errors.New("client")

func TestEnd(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := newProvider(Config{ServiceName: "svc", Version: "dev", SampleRatio: 1}, rec)
	tr := tp.Tracer("test")
	isClient := func(err error) bool { return errors.Is(err, errClient) }

	_, ok := tr.Start(context.Background(), "ok")
	End(ok, nil, isClient)
	_, client := tr.Start(context.Background(), "client")
	End(client, errClient, isClient)
	_, failed := tr.Start(context.Background(), "failed")
	End(failed, errors.New("boom"), isClient)

	spans := rec.Ended()
	require.Len(t, spans, 3)
	assert.Equal(t, codes.Unset, spans[0].Status().Code)
	assert.Equal(t, codes.Unset, spans[1].Status().Code)
	assert.Equal(t, "client", spans[1].Attributes()[0].Value.AsString())
	assert.Equal(t, codes.Error, spans[2].Status().Code)
	assert.Equal(t, "boom", spans[2].Status().Description)
	require.Len(t, spans[2].Events(), 1)
	assert.Equal(t, "exception", spans[2].Events()[0].Name)
}

func TestSetupWithEndpoint(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	shutdown, err := Setup(context.Background(), Config{ServiceName: "svc", Endpoint: "http://127.0.0.1:4317", SampleRatio: 1})
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	assert.True(t, span.IsRecording())
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)

	for _, bad := range []string{"://bad", "jaeger:4317", "ftp://jaeger:4317"} {
		_, err = Setup(context.Background(), Config{ServiceName: "svc", Endpoint: bad, SampleRatio: 1})
		assert.Error(t, err, bad)
	}
	for _, ratio := range []float64{-0.1, 1.5, math.NaN()} {
		_, err = Setup(context.Background(), Config{ServiceName: "svc", Endpoint: "http://127.0.0.1:4317", SampleRatio: ratio})
		assert.Error(t, err, ratio)
	}
}

func TestSampleRatio(t *testing.T) {
	remote := func(flags string) context.Context {
		carrier := propagation.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-" + flags}
		return propagation.TraceContext{}.Extract(context.Background(), carrier)
	}
	sampled := func(ratio float64, ctx context.Context) bool {
		tp := newProvider(Config{ServiceName: "svc", SampleRatio: ratio}, tracetest.NewSpanRecorder())
		_, span := tp.Tracer("test").Start(ctx, "op")
		defer span.End()
		return span.SpanContext().IsSampled()
	}

	assert.False(t, sampled(0, context.Background()), "new trace at ratio 0")
	assert.True(t, sampled(1, context.Background()), "new trace at ratio 1")
	assert.True(t, sampled(0, remote("01")), "sampled parent is followed")
	assert.False(t, sampled(1, remote("00")), "unsampled parent is followed")
}
