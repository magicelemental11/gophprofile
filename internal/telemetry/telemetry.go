// Package telemetry sets up OpenTelemetry tracing for the server and the worker.
package telemetry

import (
	"context"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Config describes the traced service and where its spans go.
type Config struct {
	ServiceName string
	Version     string
	// Endpoint is an OTLP/gRPC URL such as http://jaeger:4317; empty disables export.
	Endpoint string
	// SampleRatio is the share of new traces that are recorded, from 0 to 1. A trace started
	// upstream keeps the sampling decision of its parent.
	SampleRatio float64
}

// Setup installs the global W3C trace context propagator and, when an endpoint is set, a global
// tracer provider exporting spans over OTLP/gRPC. Without an endpoint spans are not recorded,
// but incoming trace context is still passed on. The returned function flushes pending spans.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if cfg.Endpoint == "" {
		return setup(cfg, nil), nil
	}
	if !(cfg.SampleRatio >= 0 && cfg.SampleRatio <= 1) {
		return nil, fmt.Errorf("trace sample ratio %g must be between 0 and 1", cfg.SampleRatio)
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("OTLP endpoint %q must be an http:// or https:// URL", cfg.Endpoint)
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}
	return setup(cfg, exp), nil
}

func setup(cfg Config, exp sdktrace.SpanExporter) func(context.Context) error {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if exp == nil {
		return func(context.Context) error { return nil }
	}
	tp := newProvider(cfg, sdktrace.NewBatchSpanProcessor(exp))
	otel.SetTracerProvider(tp)
	return tp.Shutdown
}

func newProvider(cfg Config, sp sdktrace.SpanProcessor) *sdktrace.TracerProvider {
	res, _ := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.Version),
	))
	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithSpanProcessor(sp),
	)
}

// End ends span, marking it failed when err is a server-side error. Errors for which
// isClient reports true are the caller's fault and only recorded as an attribute.
func End(span trace.Span, err error, isClient func(error) bool) {
	switch {
	case err == nil:
	case isClient != nil && isClient(err):
		span.SetAttributes(attribute.String("error.kind", "client"))
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
