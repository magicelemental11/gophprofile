// Package metrics exposes Prometheus metrics of the server and the worker.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

var (
	httpBuckets   = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
	uploadBuckets = []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30}
)

// Registry holds the application metrics and the Prometheus registry they are exposed from.
type Registry struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight prometheus.Gauge

	uploads        *prometheus.CounterVec
	uploadDuration *prometheus.HistogramVec
	deletes        *prometheus.CounterVec

	processed       *prometheus.CounterVec
	processDuration *prometheus.HistogramVec
	retries         *prometheus.CounterVec
	deadLettered    *prometheus.CounterVec
}

// New returns metrics registered in a fresh registry together with Go runtime and process metrics.
func New() *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := factory{reg}
	m := &Registry{
		reg: reg,
		httpRequests: f.counter("http_requests_total", "HTTP requests by method, route and status code.",
			"method", "route", "code"),
		httpDuration: f.histogram("http_request_duration_seconds", "HTTP request duration.", httpBuckets,
			"method", "route"),
		httpInFlight: f.gauge("http_requests_in_flight", "HTTP requests being served."),
		uploads: f.counter("avatars_uploads_total", "Avatar uploads by outcome: success, rejected or error.",
			"status"),
		uploadDuration: f.histogram("avatars_upload_duration_seconds", "Avatar upload duration by outcome.", uploadBuckets,
			"status"),
		deletes: f.counter("avatars_deletes_total", "Avatar deletions by outcome.", "status"),
		processed: f.counter("avatars_processing_total", "Broker messages handled by the worker by operation and outcome.",
			"operation", "status"),
		processDuration: f.histogram("avatars_processing_duration_seconds", "Message handling duration, retries included.", uploadBuckets,
			"operation"),
		retries:      f.counter("worker_retries_total", "Retried message handling attempts.", "operation"),
		deadLettered: f.counter("worker_dead_lettered_total", "Messages given up on and sent to the dead-letter queue.", "operation"),
	}
	m.initSeries()
	return m
}

func (m *Registry) initSeries() {
	for _, o := range domain.Outcomes {
		m.uploads.WithLabelValues(string(o))
		m.deletes.WithLabelValues(string(o))
	}
	for _, op := range []string{domain.OperationProcess, domain.OperationDelete} {
		m.retries.WithLabelValues(op)
		m.deadLettered.WithLabelValues(op)
		for _, o := range domain.MessageOutcomes {
			m.processed.WithLabelValues(op, string(o))
		}
	}
}

// Register adds collectors to the registry.
func (m *Registry) Register(cs ...prometheus.Collector) error {
	for _, c := range cs {
		if err := m.reg.Register(c); err != nil {
			return fmt.Errorf("register collector: %w", err)
		}
	}
	return nil
}

// Handler serves the metrics in the Prometheus text or OpenMetrics format. A failing collector
// is reported but does not hide the other metrics.
func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		ErrorHandling:     promhttp.ContinueOnError,
	})
}

// RequestStarted counts a request in flight until RequestFinished.
func (m *Registry) RequestStarted() {
	m.httpInFlight.Inc()
}

// RequestFinished records a served request; route is the route template, not the raw path.
func (m *Registry) RequestFinished(ctx context.Context, method, route string, code int, d time.Duration) {
	m.httpInFlight.Dec()
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(code)).Inc()
	observe(ctx, m.httpDuration.WithLabelValues(method, route), d)
}

// ObserveUpload records an upload attempt and how long it took.
func (m *Registry) ObserveUpload(ctx context.Context, outcome domain.Outcome, d time.Duration) {
	m.uploads.WithLabelValues(string(outcome)).Inc()
	observe(ctx, m.uploadDuration.WithLabelValues(string(outcome)), d)
}

// ObserveDelete records a deletion attempt.
func (m *Registry) ObserveDelete(outcome domain.Outcome) {
	m.deletes.WithLabelValues(string(outcome)).Inc()
}

// ObserveMessage records a message handled by the worker.
func (m *Registry) ObserveMessage(ctx context.Context, operation string, outcome domain.Outcome, d time.Duration) {
	m.processed.WithLabelValues(operation, string(outcome)).Inc()
	observe(ctx, m.processDuration.WithLabelValues(operation), d)
}

// Retried counts a failed attempt that is going to be repeated.
func (m *Registry) Retried(operation string) {
	m.retries.WithLabelValues(operation).Inc()
}

// DeadLettered counts a message sent to the dead-letter queue.
func (m *Registry) DeadLettered(operation string) {
	m.deadLettered.WithLabelValues(operation).Inc()
}

func observe(ctx context.Context, o prometheus.Observer, d time.Duration) {
	sc := trace.SpanContextFromContext(ctx)
	if eo, ok := o.(prometheus.ExemplarObserver); ok && sc.IsSampled() {
		eo.ObserveWithExemplar(d.Seconds(), prometheus.Labels{"trace_id": sc.TraceID().String()})
		return
	}
	o.Observe(d.Seconds())
}

// Serve exposes h at /metrics on addr until ctx is done.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen metrics: %w", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return fmt.Errorf("serve metrics: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown metrics: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve metrics: %w", err)
	}
	return nil
}

type factory struct{ reg prometheus.Registerer }

func (f factory) counter(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) histogram(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	f.reg.MustRegister(h)
	return h
}

func (f factory) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.reg.MustRegister(g)
	return g
}
