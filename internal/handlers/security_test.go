package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func secureRouter(t *testing.T, svc *fakeService, sec Security) http.Handler {
	t.Helper()
	web, err := NewWeb(testWebFS)
	require.NoError(t, err)
	return NewRouter(Deps{Logger: zap.NewNop(), Avatars: svc, Web: web, Security: sec})
}

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUploadRateLimitSharedByAPIAndWeb(t *testing.T) {
	h := secureRouter(t, &fakeService{}, Security{UploadRateLimit: 2})

	assert.Equal(t, http.StatusCreated, do(h, multipartRequest(t, "file", []byte("img"))).Code)
	assert.Equal(t, http.StatusSeeOther, do(h, webUploadRequest(t, "gopher", []byte("img"))).Code)

	rec := do(h, multipartRequest(t, "file", []byte("img")))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.JSONEq(t, `{"error":"Too many requests"}`, rec.Body.String())
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusTooManyRequests, do(h, webUploadRequest(t, "gopher", []byte("img"))).Code)

	other := multipartRequest(t, "file", []byte("img"))
	other.RemoteAddr = "198.51.100.7:4000"
	assert.Equal(t, http.StatusCreated, do(h, other).Code)
}

func TestRateLimitRPS(t *testing.T) {
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": testAvatar()}}
	h := secureRouter(t, svc, Security{RateLimitRPS: 2})
	for range 2 {
		assert.Equal(t, http.StatusOK, do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil)).Code)
	}
	rec := do(h, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusOK, do(h, httptest.NewRequest(http.MethodGet, "/health", nil)).Code)
}

func preflight(path, origin, method string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)
	req.Header.Set("Access-Control-Request-Headers", "X-User-ID, Content-Type")
	return req
}

func TestCORSPreflight(t *testing.T) {
	h := secureRouter(t, &fakeService{}, Security{CORSAllowedOrigins: []string{"https://app.example"}})

	rec := do(h, preflight("/api/v1/avatars", "https://app.example", http.MethodPost))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "https://app.example", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "POST", rec.Header().Get("Access-Control-Allow-Methods"))
	assert.Contains(t, strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")), "x-user-id")

	rec = do(h, preflight("/api/v1/avatars/a1", "https://evil.example", http.MethodDelete))
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))

	rec = do(h, preflight("/api/v1/avatars/a1", "https://app.example", http.MethodPut))
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSImagesOpenToAnyOrigin(t *testing.T) {
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": testAvatar()}}
	h := secureRouter(t, svc, Security{CORSAllowedOrigins: []string{"https://app.example"}})

	for _, path := range []string{"/api/v1/avatars/a1", "/api/v1/users/gopher/avatar"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Origin", "https://blog.example")
		rec := do(h, req)
		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "https://blog.example", rec.Header().Get("Access-Control-Allow-Origin"), path)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil)
	req.Header.Set("Origin", "https://blog.example")
	assert.Empty(t, do(h, req).Header().Get("Access-Control-Allow-Origin"))
}

func TestInvalidUserIDHeader(t *testing.T) {
	for _, uid := range []string{"a b", "<script>", "юзер", strings.Repeat("x", 256)} {
		upload := multipartRequest(t, "file", []byte("img"))
		upload.Header.Set("X-User-ID", uid)
		for _, req := range []*http.Request{upload, deleteRequest("/api/v1/avatars/a1", uid), deleteRequest("/api/v1/users/gopher/avatar", uid)} {
			svc := &fakeService{}
			rec := serve(svc, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s %s", req.Method, req.URL.Path)
			assert.JSONEq(t, `{"error":"Invalid user id"}`, rec.Body.String())
			assert.Nil(t, svc.uploaded)
			assert.Empty(t, svc.deleted.requester)
		}
	}
}

func TestInvalidUserIDInPath(t *testing.T) {
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/users/a%20b/avatar", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/users/%3Cscript%3E/avatars", nil),
		deleteRequest("/api/v1/users/a%2Fb/avatar", "gopher"),
	} {
		rec := serve(&fakeService{}, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, req.URL.String())
		assert.JSONEq(t, `{"error":"Invalid user id"}`, rec.Body.String())
	}
}

func TestWebUploadInvalidUserID(t *testing.T) {
	svc := &fakeService{}
	rec := serveWeb(t, svc, webUploadRequest(t, "a b", []byte("img")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid user ID")
	assert.Nil(t, svc.uploaded)
}

func TestImageSecurityHeaders(t *testing.T) {
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": testAvatar()}}
	for _, path := range []string{"/api/v1/avatars/a1", "/api/v1/users/gopher/avatar"} {
		rec := serve(svc, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), path)
		assert.Equal(t, "inline", rec.Header().Get("Content-Disposition"), path)
	}
	svc.openErr = domain.ErrNotFound
	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/users/nobody/avatar", nil))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "inline", rec.Header().Get("Content-Disposition"))
}
