package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type pingFunc func(ctx context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func ok() Pinger { return pingFunc(func(context.Context) error { return nil }) }

func TestHealthAllOK(t *testing.T) {
	h := NewRouter(Deps{Logger: zap.NewNop(), Health: map[string]Pinger{"database": ok(), "storage": ok(), "broker": ok()}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok","components":{"database":"ok","storage":"ok","broker":"ok"}}`, rec.Body.String())
}

func TestHealthDegraded(t *testing.T) {
	failing := pingFunc(func(context.Context) error { return errors.New("connection refused") })
	h := NewRouter(Deps{Logger: zap.NewNop(), Health: map[string]Pinger{"database": ok(), "storage": failing}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"status":"degraded","components":{"database":"ok","storage":"error: connection refused"}}`, rec.Body.String())
}

func TestHealthTimeout(t *testing.T) {
	slow := pingFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	h := health(map[string]Pinger{"broker": slow}, 1)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "deadline exceeded")
}
