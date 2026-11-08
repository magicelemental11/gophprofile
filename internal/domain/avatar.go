// Package domain defines avatars, their statuses, sizes, formats, events and errors.
package domain

import (
	"fmt"
	"time"
)

// UploadStatus tracks whether the original file reached object storage.
type UploadStatus string

// Upload statuses: uploading until the original is stored, then uploaded or failed.
const (
	UploadUploading UploadStatus = "uploading"
	UploadUploaded  UploadStatus = "uploaded"
	UploadFailed    UploadStatus = "failed"
)

// ProcessingStatus tracks thumbnail generation.
type ProcessingStatus string

// Processing statuses: pending until the worker takes the avatar, completed once all thumbnails exist.
const (
	ProcessingPending    ProcessingStatus = "pending"
	ProcessingProcessing ProcessingStatus = "processing"
	ProcessingCompleted  ProcessingStatus = "completed"
	ProcessingFailed     ProcessingStatus = "failed"
)

// Statuses reported to API clients.
const (
	StatusProcessing = "processing"
	StatusReady      = "ready"
	StatusFailed     = "failed"
)

// Size is a requested avatar variant.
type Size string

// Sizes accepted by the size query parameter.
const (
	SizeOriginal Size = "original"
	Size100      Size = "100x100"
	Size300      Size = "300x300"
)

// ThumbnailSizes lists the thumbnails the worker generates for every avatar.
var ThumbnailSizes = []Size{Size100, Size300}

// ParseSize converts a query value into a Size; an empty value means the original.
func ParseSize(s string) (Size, error) {
	switch Size(s) {
	case "", SizeOriginal:
		return SizeOriginal, nil
	case Size100, Size300:
		return Size(s), nil
	}
	return "", fmt.Errorf("%w: size %q, supported: 100x100, 300x300, original", ErrInvalidParam, s)
}

// Dimensions returns the thumbnail width and height, or zeros for the original.
func (s Size) Dimensions() (width, height int) {
	switch s {
	case Size100:
		return 100, 100
	case Size300:
		return 300, 300
	}
	return 0, 0
}

// Format is an image encoding.
type Format string

// Formats accepted for upload and by the format query parameter.
const (
	FormatJPEG Format = "jpeg"
	FormatPNG  Format = "png"
	FormatWebP Format = "webp"
)

// ParseFormat converts a query value into a Format; an empty value keeps the stored format.
func ParseFormat(s string) (Format, error) {
	switch s {
	case "":
		return "", nil
	case "jpeg", "jpg":
		return FormatJPEG, nil
	case "png":
		return FormatPNG, nil
	case "webp":
		return FormatWebP, nil
	}
	return "", fmt.Errorf("%w: format %q, supported: jpeg, png, webp", ErrInvalidParam, s)
}

// MIME returns the media type of the format.
func (f Format) MIME() string {
	return "image/" + string(f)
}

// Ext returns the file extension used for stored objects.
func (f Format) Ext() string {
	if f == FormatJPEG {
		return "jpg"
	}
	return string(f)
}

var formats = []Format{FormatJPEG, FormatPNG, FormatWebP}

// FormatFromMIME maps a media type back to a supported Format.
func FormatFromMIME(mime string) (Format, bool) {
	for _, f := range formats {
		if f.MIME() == mime {
			return f, true
		}
	}
	return "", false
}

// Avatar is the metadata of one uploaded image.
type Avatar struct {
	ID               string
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	Width            int
	Height           int
	S3Key            string
	ThumbnailKeys    map[Size]string
	ETag             string
	UploadStatus     UploadStatus
	ProcessingStatus ProcessingStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Status summarizes upload and processing into processing, ready or failed.
func (a Avatar) Status() string {
	switch {
	case a.UploadStatus == UploadFailed || a.ProcessingStatus == ProcessingFailed:
		return StatusFailed
	case a.ProcessingStatus == ProcessingCompleted:
		return StatusReady
	}
	return StatusProcessing
}

// AllKeys returns every object key the avatar may own: the original and all thumbnails.
func (a Avatar) AllKeys() []string {
	return append([]string{a.S3Key}, ThumbnailKeysOf(a.ID)...)
}

// ThumbnailKeysOf returns the keys of every thumbnail the avatar may have, generated or not.
func ThumbnailKeysOf(avatarID string) []string {
	keys := make([]string, 0, len(ThumbnailSizes))
	for _, s := range ThumbnailSizes {
		keys = append(keys, ThumbnailKey(avatarID, s))
	}
	return keys
}

// OwnsKey reports whether key is the original or a thumbnail of the avatar.
func OwnsKey(avatarID, key string) bool {
	for _, f := range formats {
		if key == OriginalKey(avatarID, f) {
			return true
		}
	}
	for _, s := range ThumbnailSizes {
		if key == ThumbnailKey(avatarID, s) {
			return true
		}
	}
	return false
}

// OriginalKey returns the object key of the uploaded file.
func OriginalKey(avatarID string, f Format) string {
	return "originals/" + avatarID + "." + f.Ext()
}

// ThumbnailKey returns the object key of a generated thumbnail.
func ThumbnailKey(avatarID string, s Size) string {
	return "thumbnails/" + avatarID + "/" + string(s) + ".jpg"
}
