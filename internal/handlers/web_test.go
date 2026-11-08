package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

var testWebFS = fstest.MapFS{
	"index.html":    {Data: []byte(`<html>page user={{.UserID}} error={{.Error}}</html>`)},
	"static/app.js": {Data: []byte(`console.log("app")`)},
}

func serveWeb(t *testing.T, svc *fakeService, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	web, err := NewWeb(testWebFS)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	NewRouter(Deps{Logger: zap.NewNop(), Avatars: svc, Web: web}).ServeHTTP(rec, req)
	return rec
}

func webUploadRequest(t *testing.T, userID string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("user_id", userID))
	if data != nil {
		fw, err := mw.CreateFormFile("file", "cat.png")
		require.NoError(t, err)
		_, err = fw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/web/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestNewWebRequiresIndex(t *testing.T) {
	_, err := NewWeb(fstest.MapFS{})
	assert.Error(t, err)
}

func TestWebRootRedirects(t *testing.T) {
	rec := serveWeb(t, &fakeService{}, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/web/upload", rec.Header().Get("Location"))
}

func TestWebPages(t *testing.T) {
	for _, path := range []string{"/web/upload", "/web/gallery/gopher@example.com"} {
		t.Run(path, func(t *testing.T) {
			rec := serveWeb(t, &fakeService{}, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), "<html>page")
		})
	}
}

func TestWebStatic(t *testing.T) {
	rec := serveWeb(t, &fakeService{}, httptest.NewRequest(http.MethodGet, "/web/static/app.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `console.log("app")`, rec.Body.String())
}

func TestWebUploadRedirectsToGallery(t *testing.T) {
	svc := &fakeService{}
	rec := serveWeb(t, svc, webUploadRequest(t, "gopher@example.com", []byte("png bytes")))

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "/web/gallery/gopher@example.com", rec.Header().Get("Location"))
	assert.Equal(t, "gopher@example.com", svc.uploadUser)
	assert.Equal(t, []byte("png bytes"), svc.uploaded)
	assert.Equal(t, "cat.png", svc.uploadName)
}

func TestWebUploadEscapesUserInLocation(t *testing.T) {
	rec := serveWeb(t, &fakeService{}, webUploadRequest(t, "a/b c", []byte("x")))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/web/gallery/a%2Fb%20c", rec.Header().Get("Location"))
}

func TestWebUploadErrors(t *testing.T) {
	tests := map[string]struct {
		req    func(t *testing.T) *http.Request
		svcErr error
		code   int
		msg    string
	}{
		"bad format": {
			req:    func(t *testing.T) *http.Request { return webUploadRequest(t, "u1", []byte("x")) },
			svcErr: domain.ErrInvalidFormat,
			code:   http.StatusBadRequest, msg: "Invalid file format",
		},
		"no user": {
			req:  func(t *testing.T) *http.Request { return webUploadRequest(t, " ", []byte("x")) },
			code: http.StatusBadRequest, msg: "Missing user ID",
		},
		"no file": {
			req:  func(t *testing.T) *http.Request { return webUploadRequest(t, "u1", nil) },
			code: http.StatusBadRequest, msg: "Missing file field",
		},
		"too large": {
			req:  func(t *testing.T) *http.Request { return webUploadRequest(t, "u1", make([]byte, testMaxSize+2<<20)) },
			code: http.StatusRequestEntityTooLarge, msg: "File too large",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := serveWeb(t, &fakeService{uploadErr: tt.svcErr}, tt.req(t))
			assert.Equal(t, tt.code, rec.Code)
			assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), "error="+tt.msg)
		})
	}
}

func TestWebUploadErrorKeepsUserID(t *testing.T) {
	rec := serveWeb(t, &fakeService{uploadErr: domain.ErrInvalidFormat}, webUploadRequest(t, "<u1>", []byte("x")))
	assert.Contains(t, rec.Body.String(), "user=&lt;u1&gt;")
}
