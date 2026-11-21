package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

const maxUserIDField = 1 << 10

var (
	errNoUser      = errors.New("missing user id field")
	errInvalidUser = errors.New("invalid user id")
)

// Web holds the web interface: the page template and the static assets.
type Web struct {
	page   *template.Template
	static http.Handler
}

type pageData struct {
	UserID string
	Error  string
}

// NewWeb loads the web interface from dir, which must contain index.html and a static directory.
func NewWeb(dir fs.FS) (*Web, error) {
	page, err := template.ParseFS(dir, "index.html")
	if err != nil {
		return nil, fmt.Errorf("parse web page: %w", err)
	}
	static, err := fs.Sub(dir, "static")
	if err != nil {
		return nil, fmt.Errorf("web static: %w", err)
	}
	return &Web{
		page:   page,
		static: http.StripPrefix("/web/static/", http.FileServer(http.FS(static))),
	}, nil
}

type webHandler struct {
	*api
	web *Web
}

func (h *webHandler) mount(r chi.Router, uploadLimit func(http.Handler) http.Handler) {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/upload", http.StatusFound)
	})
	r.Get("/web/upload", h.index)
	r.Get("/web/gallery/{user_id}", h.index)
	r.With(uploadLimit).Post("/web/upload", h.upload)
	r.Handle("/web/static/*", h.web.static)
}

func (h *webHandler) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, pageData{})
}

func (h *webHandler) upload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	uid, file, err := h.receiveForm(w, r)
	if err != nil {
		h.uploads.ObserveUpload(r.Context(), domain.OutcomeRejected, time.Since(start))
	} else {
		defer file.Close()
		_, err = h.svc.Upload(r.Context(), uid, file.FileName(), file)
	}
	switch {
	case errors.Is(err, errNoUser):
		h.render(w, r, http.StatusBadRequest, pageData{Error: "Missing user ID"})
	case errors.Is(err, errInvalidUser):
		h.render(w, r, http.StatusBadRequest, pageData{UserID: uid, Error: "Invalid user ID"})
	case err != nil:
		h.failPage(w, r, uid, err)
	default:
		http.Redirect(w, r, "/web/gallery/"+url.PathEscape(uid), http.StatusSeeOther)
	}
}

func (h *webHandler) receiveForm(w http.ResponseWriter, r *http.Request) (string, *multipart.Part, error) {
	r.Body = http.MaxBytesReader(w, r.Body, h.svc.MaxSize()+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, errNotMultipart
	}
	var uid string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			if uid == "" {
				return "", nil, errNoUser
			}
			return uid, nil, errNoFile
		}
		if err != nil {
			return uid, nil, fmt.Errorf("%w: %w", domain.ErrIncompleteUpload, err)
		}
		switch part.FormName() {
		case "user_id":
			v, err := io.ReadAll(io.LimitReader(part, maxUserIDField))
			_ = part.Close()
			if err != nil {
				return "", nil, fmt.Errorf("%w: %w", domain.ErrIncompleteUpload, err)
			}
			uid = strings.TrimSpace(string(v))
		case "file":
			switch {
			case uid == "":
				_ = part.Close()
				return "", nil, errNoUser
			case !domain.ValidUserID(uid):
				_ = part.Close()
				return uid, nil, errInvalidUser
			}
			return uid, part, nil
		default:
			_ = part.Close()
		}
	}
}

func (h *webHandler) failPage(w http.ResponseWriter, r *http.Request, uid string, err error) {
	code, body := h.classify(r.Context(), err)
	msg := body.Error
	if body.Details != "" {
		msg += ": " + body.Details
	}
	h.render(w, r, code, pageData{UserID: uid, Error: msg})
}

func (h *webHandler) render(w http.ResponseWriter, r *http.Request, code int, data pageData) {
	var buf bytes.Buffer
	if err := h.web.page.Execute(&buf, data); err != nil {
		h.log.ErrorContext(r.Context(), "render page", slog.Any("error", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}
