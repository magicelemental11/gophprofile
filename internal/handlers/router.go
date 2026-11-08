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
	Logger  *zap.Logger
	Health  map[string]Pinger
	Avatars AvatarService
	Web     *Web
}

// NewRouter builds the HTTP handler with all routes and middleware.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, logRequests(d.Logger), middleware.Recoverer, middleware.Timeout(requestTimeout))

	r.Get("/health", health(d.Health, healthTimeout))

	h := &api{svc: d.Avatars, log: d.Logger}
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/avatars", h.upload)
		r.Get("/avatars/{id}", h.image)
		r.Delete("/avatars/{id}", h.delete)
		r.Get("/avatars/{id}/metadata", h.metadata)
		r.Get("/users/{user_id}/avatar", h.userImage)
		r.Delete("/users/{user_id}/avatar", h.deleteCurrent)
		r.Get("/users/{user_id}/avatars", h.list)
	})
	if d.Web != nil {
		(&webHandler{api: h, web: d.Web}).mount(r)
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
