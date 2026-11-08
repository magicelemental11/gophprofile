// Package handlers exposes the REST API and the web interface over HTTP.
package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

const requestTimeout = 30 * time.Second

// Deps holds what the router needs to serve requests.
type Deps struct {
	Logger   *zap.Logger
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
		middleware.ClientIPFromRemoteAddr,
		logRequests(d.Logger),
		middleware.Recoverer,
		middleware.Timeout(requestTimeout),
		noSniff,
		corsHandler(d.Security.CORSAllowedOrigins),
	)

	r.Get("/health", health(d.Logger, d.Health, healthTimeout))

	h := &api{svc: d.Avatars, log: d.Logger}
	uploadLimit := rateLimit(d.Security.UploadRateLimit, time.Minute)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(rateLimit(d.Security.APIRateLimit, time.Second))
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
	return r
}

func logRequests(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.Status()),
				zap.Int("bytes", ww.BytesWritten()),
				zap.Duration("duration", time.Since(start)),
				zap.String("request_id", middleware.GetReqID(r.Context())),
			}
			if ww.Status() >= http.StatusInternalServerError {
				log.Error("request", fields...)
				return
			}
			log.Info("request", fields...)
		})
	}
}
