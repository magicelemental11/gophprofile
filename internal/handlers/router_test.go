package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func TestUnknownRoute(t *testing.T) {
	h := NewRouter(Deps{Logger: slog.New(slog.DiscardHandler)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	svc := &fakeService{avatars: map[string]domain.Avatar{}}
	h := NewRouter(Deps{Logger: log, Avatars: svc})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/avatars/nope/metadata", nil))

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "request", rec["msg"])
	assert.Equal(t, http.MethodGet, rec["method"])
	assert.Equal(t, "/api/v1/avatars/{id}/metadata", rec["route"])
	assert.Equal(t, float64(http.StatusNotFound), rec["status"])
	assert.Contains(t, rec, "duration")
	assert.Contains(t, rec, "request_id")
}

type finished struct {
	method, route string
	code          int
}

type fakeObserver struct {
	started  int
	finished []finished
}

func (o *fakeObserver) RequestStarted() { o.started++ }

func (o *fakeObserver) RequestFinished(_ context.Context, method, route string, code int, _ time.Duration) {
	o.finished = append(o.finished, finished{method, route, code})
}

func TestRequestMetrics(t *testing.T) {
	obs := &fakeObserver{}
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": {ID: "a1", CreatedAt: created}}}
	h := NewRouter(Deps{Logger: discardLogger(), Avatars: svc, Metrics: obs})

	for _, path := range []string{"/api/v1/avatars/a1/metadata", "/api/v1/avatars/nope/metadata", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	assert.Equal(t, 3, obs.started)
	assert.Equal(t, []finished{
		{http.MethodGet, "/api/v1/avatars/{id}/metadata", http.StatusOK},
		{http.MethodGet, "/api/v1/avatars/{id}/metadata", http.StatusNotFound},
		{http.MethodGet, "unmatched", http.StatusNotFound},
	}, obs.finished)
}
