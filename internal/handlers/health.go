package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

const healthTimeout = 2 * time.Second

// Pinger is a dependency whose availability is reported by /health.
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
}

func health(log *zap.Logger, checks map[string]Pinger, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		resp := healthResponse{Status: "ok", Components: make(map[string]string, len(checks))}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, p := range checks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				state := "ok"
				if err := p.Ping(ctx); err != nil {
					log.Warn("health check failed", zap.String("component", name), zap.Error(err))
					state = "error"
				}
				mu.Lock()
				resp.Components[name] = state
				mu.Unlock()
			}()
		}
		wg.Wait()

		status := http.StatusOK
		for _, state := range resp.Components {
			if state != "ok" {
				resp.Status = "degraded"
				status = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, status, resp)
	}
}
