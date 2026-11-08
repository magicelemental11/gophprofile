package handlers

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/service"
)

const multipartOverhead = 1 << 20

//go:embed assets/placeholder.png
var placeholder []byte

// AvatarService is the use-case layer the API calls.
type AvatarService interface {
	MaxSize() int64
	Upload(ctx context.Context, userID, fileName string, r io.Reader) (domain.Avatar, error)
	Get(ctx context.Context, id string) (domain.Avatar, error)
	List(ctx context.Context, userID string) ([]domain.Avatar, error)
	Open(ctx context.Context, id string, size domain.Size, format domain.Format) (service.Image, error)
	OpenLatest(ctx context.Context, userID string, size domain.Size, format domain.Format) (service.Image, error)
	Delete(ctx context.Context, requesterID, avatarID string) error
	DeleteCurrent(ctx context.Context, requesterID, userID string) error
}

type api struct {
	svc AvatarService
	log *zap.Logger
}

type uploadResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type thumbnail struct {
	Size domain.Size `json:"size"`
	URL  string      `json:"url"`
}

type metadataResponse struct {
	ID         string      `json:"id"`
	UserID     string      `json:"user_id"`
	FileName   string      `json:"file_name"`
	MimeType   string      `json:"mime_type"`
	Size       int64       `json:"size"`
	Dimensions dimensions  `json:"dimensions"`
	Thumbnails []thumbnail `json:"thumbnails"`
	Status     string      `json:"status"`
	URL        string      `json:"url"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

func avatarURL(id string) string {
	return "/api/v1/avatars/" + id
}

func metadata(a domain.Avatar) metadataResponse {
	thumbs := []thumbnail{}
	for _, s := range domain.ThumbnailSizes {
		if _, ok := a.ThumbnailKeys[s]; ok {
			thumbs = append(thumbs, thumbnail{Size: s, URL: avatarURL(a.ID) + "?size=" + string(s)})
		}
	}
	return metadataResponse{
		ID: a.ID, UserID: a.UserID, FileName: a.FileName, MimeType: a.MimeType, Size: a.SizeBytes,
		Dimensions: dimensions{Width: a.Width, Height: a.Height},
		Thumbnails: thumbs,
		Status:     a.Status(),
		URL:        avatarURL(a.ID),
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
	}
}

func pathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

func userID(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-User-ID"))
}

func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid := userID(r)
	if uid == "" {
		writeError(w, http.StatusBadRequest, "Missing X-User-ID header", "")
		return "", false
	}
	return uid, true
}

func (h *api) upload(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}
	a, err := h.receive(w, r, uid)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Location", avatarURL(a.ID))
	writeJSON(w, http.StatusCreated, uploadResponse{
		ID: a.ID, UserID: a.UserID, URL: avatarURL(a.ID), Status: a.Status(), CreatedAt: a.CreatedAt,
	})
}

var (
	errNotMultipart = errors.New("not a multipart form")
	errNoFile       = errors.New("missing file field")
)

func (h *api) receive(w http.ResponseWriter, r *http.Request, uid string) (domain.Avatar, error) {
	r.Body = http.MaxBytesReader(w, r.Body, h.svc.MaxSize()+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		return domain.Avatar{}, errNotMultipart
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return domain.Avatar{}, errNoFile
		}
		if err != nil {
			return domain.Avatar{}, err
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		defer part.Close()
		return h.svc.Upload(r.Context(), uid, part.FileName(), part)
	}
}

func parseVariant(r *http.Request) (domain.Size, domain.Format, error) {
	size, err := domain.ParseSize(r.URL.Query().Get("size"))
	if err != nil {
		return "", "", err
	}
	format, err := domain.ParseFormat(r.URL.Query().Get("format"))
	if err != nil {
		return "", "", err
	}
	return size, format, nil
}

func (h *api) image(w http.ResponseWriter, r *http.Request) {
	size, format, err := parseVariant(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	img, err := h.svc.Open(r.Context(), pathParam(r, "id"), size, format)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.sendImage(w, r, img)
}

func (h *api) userImage(w http.ResponseWriter, r *http.Request) {
	size, format, err := parseVariant(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	img, err := h.svc.OpenLatest(r.Context(), pathParam(r, "user_id"), size, format)
	if errors.Is(err, domain.ErrNotFound) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(placeholder)
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.sendImage(w, r, img)
}

func (h *api) sendImage(w http.ResponseWriter, r *http.Request, img service.Image) {
	defer img.Body.Close()
	w.Header().Set("ETag", img.ETag)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if r.Header.Get("If-None-Match") == img.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", img.ContentType)
	if _, err := io.Copy(w, img.Body); err != nil {
		h.log.Warn("send image", zap.Error(err))
	}
}

func (h *api) metadata(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metadata(a))
}

func (h *api) list(w http.ResponseWriter, r *http.Request) {
	avatars, err := h.svc.List(r.Context(), pathParam(r, "user_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	resp := make([]metadataResponse, 0, len(avatars))
	for _, a := range avatars {
		resp = append(resp, metadata(a))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *api) delete(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}
	h.respondDeleted(w, h.svc.Delete(r.Context(), uid, pathParam(r, "id")))
}

func (h *api) deleteCurrent(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireUserID(w, r)
	if !ok {
		return
	}
	h.respondDeleted(w, h.svc.DeleteCurrent(r.Context(), uid, pathParam(r, "user_id")))
}

func (h *api) respondDeleted(w http.ResponseWriter, err error) {
	if err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *api) fail(w http.ResponseWriter, err error) {
	code, body := h.classify(err)
	writeJSON(w, code, body)
}

func (h *api) classify(err error) (int, errorBody) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, domain.ErrTooLarge), errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge, errorBody{Error: "File too large", MaxSize: h.svc.MaxSize()}
	case errors.Is(err, domain.ErrInvalidFormat):
		return http.StatusBadRequest, errorBody{Error: "Invalid file format", Details: "Supported formats: jpeg, png, webp"}
	case errors.Is(err, domain.ErrInvalidParam):
		return http.StatusBadRequest, errorBody{Error: "Invalid parameter", Details: err.Error()}
	case errors.Is(err, errNotMultipart):
		return http.StatusBadRequest, errorBody{Error: "Invalid multipart form"}
	case errors.Is(err, errNoFile):
		return http.StatusBadRequest, errorBody{Error: "Missing file field"}
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, errorBody{Error: "Avatar not found"}
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, errorBody{Error: "Forbidden", Details: "You can only delete your own avatars"}
	default:
		h.log.Error("request failed", zap.Error(err))
		return http.StatusInternalServerError, errorBody{Error: "Internal server error"}
	}
}
