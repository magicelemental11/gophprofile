package handlers

import (
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// Security holds the request limits and the CORS policy; zero limits disable rate limiting.
type Security struct {
	CORSAllowedOrigins []string
	APIRateLimit       int
	UploadRateLimit    int
}

var imagePath = regexp.MustCompile(`^/api/v1/(avatars/[^/]+|users/[^/]+/avatar)$`)

func corsHandler(allowed []string) func(http.Handler) http.Handler {
	allowAll := slices.Contains(allowed, "*")
	return cors.Handler(cors.Options{
		AllowOriginFunc: func(r *http.Request, origin string) bool {
			if allowAll || slices.Contains(allowed, origin) {
				return true
			}
			method := r.Method
			if method == http.MethodOptions {
				method = r.Header.Get("Access-Control-Request-Method")
			}
			return method == http.MethodGet && imagePath.MatchString(r.URL.Path)
		},
		AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodDelete},
		AllowedHeaders: []string{"Content-Type", "X-User-ID"},
		ExposedHeaders: []string{"Location", "ETag", "Retry-After"},
		MaxAge:         300,
	})
}

func rateLimit(limit int, window time.Duration) func(http.Handler) http.Handler {
	if limit <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return httprate.LimitBy(limit, window,
		func(r *http.Request) (string, error) {
			return httprate.CanonicalizeIP(middleware.GetClientIP(r.Context())), nil
		},
		httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusTooManyRequests, "Too many requests", "")
		}),
	)
}

func noSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func validUserPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !domain.ValidUserID(pathParam(r, "user_id")) {
			writeError(w, http.StatusBadRequest, "Invalid user id", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
