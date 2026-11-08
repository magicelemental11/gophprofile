package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/service"
)

const testMaxSize = 1024

var created = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

type fakeService struct {
	uploadErr  error
	uploaded   []byte
	uploadUser string
	uploadName string
	avatars    map[string]domain.Avatar
	openErr    error
	deleteErr  error
	deleted    struct{ requester, id, user string }
	opened     struct {
		id, user string
		size     domain.Size
		format   domain.Format
	}
}

func (f *fakeService) MaxSize() int64 { return testMaxSize }

func (f *fakeService) Upload(_ context.Context, userID, fileName string, r io.Reader) (domain.Avatar, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("read upload: %w", err)
	}
	if f.uploadErr != nil {
		return domain.Avatar{}, f.uploadErr
	}
	f.uploaded, f.uploadUser, f.uploadName = data, userID, fileName
	return domain.Avatar{ID: "a1", UserID: userID, UploadStatus: domain.UploadUploaded, ProcessingStatus: domain.ProcessingPending, CreatedAt: created}, nil
}

func (f *fakeService) Get(_ context.Context, id string) (domain.Avatar, error) {
	a, ok := f.avatars[id]
	if !ok {
		return domain.Avatar{}, domain.ErrNotFound
	}
	return a, nil
}

func (f *fakeService) List(_ context.Context, userID string) ([]domain.Avatar, error) {
	list := []domain.Avatar{}
	for _, a := range f.avatars {
		if a.UserID == userID {
			list = append(list, a)
		}
	}
	return list, nil
}

func (f *fakeService) image() (service.Image, error) {
	if f.openErr != nil {
		return service.Image{}, f.openErr
	}
	return service.Image{Body: io.NopCloser(strings.NewReader("IMG")), ContentType: "image/png", ETag: `"e1"`}, nil
}

func (f *fakeService) Open(_ context.Context, id string, size domain.Size, format domain.Format) (service.Image, error) {
	f.opened.id, f.opened.size, f.opened.format = id, size, format
	return f.image()
}

func (f *fakeService) OpenLatest(_ context.Context, userID string, size domain.Size, format domain.Format) (service.Image, error) {
	f.opened.user, f.opened.size, f.opened.format = userID, size, format
	return f.image()
}

func (f *fakeService) Delete(_ context.Context, requesterID, avatarID string) error {
	f.deleted.requester, f.deleted.id = requesterID, avatarID
	return f.deleteErr
}

func (f *fakeService) DeleteCurrent(_ context.Context, requesterID, userID string) error {
	f.deleted.requester, f.deleted.user = requesterID, userID
	return f.deleteErr
}

func serve(svc *fakeService, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	NewRouter(Deps{Logger: zap.NewNop(), Avatars: svc}).ServeHTTP(rec, req)
	return rec
}

func multipartRequest(t *testing.T, field string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(field, "cat.png")
	require.NoError(t, err)
	_, err = fw.Write(data)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-User-ID", "gopher@example.com")
	return req
}

func TestUploadCreated(t *testing.T) {
	svc := &fakeService{}
	rec := serve(svc, multipartRequest(t, "file", []byte("png bytes")))

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"id":"a1","user_id":"gopher@example.com","url":"/api/v1/avatars/a1","status":"processing","created_at":"2024-01-01T00:00:00Z"}`, rec.Body.String())
	assert.Equal(t, "/api/v1/avatars/a1", rec.Header().Get("Location"))
	assert.Equal(t, []byte("png bytes"), svc.uploaded)
	assert.Equal(t, "cat.png", svc.uploadName)
}

func TestUploadErrors(t *testing.T) {
	tests := map[string]struct {
		req    func(t *testing.T) *http.Request
		svcErr error
		code   int
		body   string
	}{
		"no user": {
			req: func(t *testing.T) *http.Request {
				r := multipartRequest(t, "file", []byte("x"))
				r.Header.Del("X-User-ID")
				return r
			},
			code: http.StatusBadRequest, body: `{"error":"Missing X-User-ID header"}`,
		},
		"not multipart": {
			req: func(*testing.T) *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", strings.NewReader("raw"))
				r.Header.Set("X-User-ID", "u")
				return r
			},
			code: http.StatusBadRequest, body: `{"error":"Invalid multipart form"}`,
		},
		"no file field": {
			req:  func(t *testing.T) *http.Request { return multipartRequest(t, "avatar", []byte("x")) },
			code: http.StatusBadRequest, body: `{"error":"Missing file field"}`,
		},
		"bad format": {
			req:    func(t *testing.T) *http.Request { return multipartRequest(t, "file", []byte("x")) },
			svcErr: domain.ErrInvalidFormat,
			code:   http.StatusBadRequest, body: `{"error":"Invalid file format","details":"Supported formats: jpeg, png, webp"}`,
		},
		"too large by service": {
			req:    func(t *testing.T) *http.Request { return multipartRequest(t, "file", []byte("x")) },
			svcErr: domain.ErrTooLarge,
			code:   http.StatusRequestEntityTooLarge, body: `{"error":"File too large","max_size":1024}`,
		},
		"body over limit": {
			req:  func(t *testing.T) *http.Request { return multipartRequest(t, "file", make([]byte, testMaxSize+2<<20)) },
			code: http.StatusRequestEntityTooLarge, body: `{"error":"File too large","max_size":1024}`,
		},
		"internal": {
			req:    func(t *testing.T) *http.Request { return multipartRequest(t, "file", []byte("x")) },
			svcErr: errors.New("db down"),
			code:   http.StatusInternalServerError, body: `{"error":"Internal server error"}`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := serve(&fakeService{uploadErr: tt.svcErr}, tt.req(t))
			assert.Equal(t, tt.code, rec.Code)
			assert.JSONEq(t, tt.body, rec.Body.String())
		})
	}
}

func TestGetAvatarImage(t *testing.T) {
	svc := &fakeService{}
	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1?size=300x300&format=webp", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "IMG", rec.Body.String())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=86400", rec.Header().Get("Cache-Control"))
	assert.Equal(t, `"e1"`, rec.Header().Get("ETag"))
	assert.Equal(t, "a1", svc.opened.id)
	assert.Equal(t, domain.Size300, svc.opened.size)
	assert.Equal(t, domain.FormatWebP, svc.opened.format)
}

func TestGetAvatarNotModified(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1", nil)
	req.Header.Set("If-None-Match", `"e1"`)
	rec := serve(&fakeService{}, req)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestGetAvatarErrors(t *testing.T) {
	rec := serve(&fakeService{openErr: domain.ErrNotFound}, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/zzz", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"error":"Avatar not found"}`, rec.Body.String())

	rec = serve(&fakeService{}, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1?size=1x1", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid parameter")

	rec = serve(&fakeService{}, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1?format=gif", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUserAvatar(t *testing.T) {
	svc := &fakeService{}
	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/users/gopher@example.com/avatar?size=100x100", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gopher@example.com", svc.opened.user)
	assert.Equal(t, domain.Size100, svc.opened.size)
}

func TestUserAvatarPlaceholder(t *testing.T) {
	rec := serve(&fakeService{openErr: domain.ErrNotFound}, httptest.NewRequest(http.MethodGet, "/api/v1/users/nobody/avatar", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, placeholder, rec.Body.Bytes())
	assert.Equal(t, "public, max-age=300", rec.Header().Get("Cache-Control"))
}

func testAvatar() domain.Avatar {
	return domain.Avatar{
		ID: "a1", UserID: "u", FileName: "cat.png", MimeType: "image/png", SizeBytes: 1024000,
		Width: 1920, Height: 1080,
		ThumbnailKeys:    map[domain.Size]string{domain.Size100: "k1", domain.Size300: "k3"},
		UploadStatus:     domain.UploadUploaded,
		ProcessingStatus: domain.ProcessingCompleted,
		CreatedAt:        created, UpdatedAt: created.Add(time.Minute),
	}
}

func TestMetadata(t *testing.T) {
	svc := &fakeService{avatars: map[string]domain.Avatar{"a1": testAvatar()}}
	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{
		"id":"a1","user_id":"u","file_name":"cat.png","mime_type":"image/png","size":1024000,
		"dimensions":{"width":1920,"height":1080},
		"thumbnails":[{"size":"100x100","url":"/api/v1/avatars/a1?size=100x100"},{"size":"300x300","url":"/api/v1/avatars/a1?size=300x300"}],
		"status":"ready","url":"/api/v1/avatars/a1",
		"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:01:00Z"}`, rec.Body.String())

	rec = serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/nope/metadata", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListUserAvatars(t *testing.T) {
	pending := testAvatar()
	pending.ID, pending.ThumbnailKeys, pending.ProcessingStatus = "a2", nil, domain.ProcessingPending
	svc := &fakeService{avatars: map[string]domain.Avatar{"a2": pending}}

	rec := serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/users/u/avatars", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "processing", list[0]["status"])
	assert.Equal(t, []any{}, list[0]["thumbnails"])

	rec = serve(svc, httptest.NewRequest(http.MethodGet, "/api/v1/users/other/avatars", nil))
	assert.JSONEq(t, `[]`, rec.Body.String())
}

func deleteRequest(path, uid string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if uid != "" {
		req.Header.Set("X-User-ID", uid)
	}
	return req
}

func TestDeleteAvatar(t *testing.T) {
	svc := &fakeService{}
	rec := serve(svc, deleteRequest("/api/v1/avatars/a1", "u"))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, "u", svc.deleted.requester)
	assert.Equal(t, "a1", svc.deleted.id)
}

func TestDeleteUserAvatar(t *testing.T) {
	svc := &fakeService{}
	rec := serve(svc, deleteRequest("/api/v1/users/u/avatar", "u"))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "u", svc.deleted.requester)
	assert.Equal(t, "u", svc.deleted.user)
}

func TestDeleteErrors(t *testing.T) {
	for _, path := range []string{"/api/v1/avatars/a1", "/api/v1/users/u/avatar"} {
		t.Run(path, func(t *testing.T) {
			svc := &fakeService{}
			rec := serve(svc, deleteRequest(path, ""))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.JSONEq(t, `{"error":"Missing X-User-ID header"}`, rec.Body.String())
			assert.Empty(t, svc.deleted.requester)

			rec = serve(&fakeService{deleteErr: domain.ErrForbidden}, deleteRequest(path, "intruder"))
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.JSONEq(t, `{"error":"Forbidden","details":"You can only delete your own avatars"}`, rec.Body.String())

			rec = serve(&fakeService{deleteErr: domain.ErrNotFound}, deleteRequest(path, "u"))
			assert.Equal(t, http.StatusNotFound, rec.Code)

			rec = serve(&fakeService{deleteErr: errors.New("db down")}, deleteRequest(path, "u"))
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
		})
	}
}
