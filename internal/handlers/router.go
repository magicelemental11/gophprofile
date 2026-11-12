// Package handlers exposes the REST API and the web interface over HTTP.
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	requestTimeout = 30 * time.Second
	unmatchedRoute = "unmatched"
)

// RequestObserver records served requests for monitoring.
type RequestObserver interface {
	RequestStarted()
	RequestFinished(ctx context.Context, method, route string, code int, d time.Duration)
}

// Deps holds what the router needs to serve requests; Metrics may be nil.
type Deps struct {
	Logger   *slog.Logger
	Metrics  RequestObserver
	Health   map[string]Pinger
	Avatars  AvatarService
	Web      *Web
	Security Security
}

// NewRouter builds the HTTP handler with all routes and middleware.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		traceRequests,
		middleware.ClientIPFromRemoteAddr,
		observeRequests(d.Logger, d.Metrics),
		middleware.Recoverer,
		middleware.Timeout(requestTimeout),
		middleware.GetHead,
		noSniff,
		corsHandler(d.Security.CORSAllowedOrigins),
	)

	r.Get("/health", health(d.Logger, d.Health, healthTimeout))

	h := &api{svc: d.Avatars, log: d.Logger}
	uploadLimit := rateLimit(d.Security.UploadRateLimit, time.Minute)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(rateLimit(d.Security.RateLimitRPS, time.Second))
		r.With(uploadLimit).Post("/avatars", h.upload)
		r.Get("/avatars/{id}", h.image)
		r.Delete("/avatars/{id}", h.delete)
		r.Get("/avatars/{id}/metadata", h.metadata)
		r.Route("/users/{user_id}", func(r chi.Router) {
			r.Use(validUserPath)
			r.Get("/avatar", h.userImage)
			r.Delete("/avatar", h.deleteCurrent)
			r.Get("/avatars", h.list)
		})
	})
	if d.Web != nil {
		(&webHandler{api: h, web: d.Web}).mount(r, uploadLimit)
	}
	return otelhttp.NewHandler(r, "http.server", otelhttp.WithFilter(traced))
}

func traced(r *http.Request) bool {
	return r.URL.Path != "/health" && !strings.HasPrefix(r.URL.Path, "/web/static/")
}

func traceRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		if sc := span.SpanContext(); sc.IsValid() {
			w.Header().Set("X-Trace-ID", sc.TraceID().String())
		}
		span.SetAttributes(attribute.String("http.request_id", middleware.GetReqID(r.Context())))
		next.ServeHTTP(w, r)
		route := routePattern(r)
		if route == unmatchedRoute {
			return
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(semconv.HTTPRoute(route))
	})
}

func observeRequests(log *slog.Logger, obs RequestObserver) func(http.Handler) http.Handler {
	if obs == nil {
		obs = nopObserver{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			obs.RequestStarted()
			next.ServeHTTP(ww, r)
			duration, route, status := time.Since(start), routePattern(r), ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			obs.RequestFinished(r.Context(), r.Method, route, status, duration)
			level := slog.LevelInfo
			switch {
			case status >= http.StatusInternalServerError:
				level = slog.LevelError
			case status >= http.StatusBadRequest:
				level = slog.LevelWarn
			}
			log.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", duration),
				slog.String("request_id", middleware.GetReqID(r.Context())),
			)
		})
	}
}

func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return unmatchedRoute
}

type nopObserver struct{}

func (nopObserver) RequestStarted()                                                     {}
func (nopObserver) RequestFinished(context.Context, string, string, int, time.Duration) {}
