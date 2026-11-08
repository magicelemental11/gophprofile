// Package imaging detects, decodes, resizes and encodes avatar images.
package imaging

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"

	"github.com/HugoSmits86/nativewebp"
	"github.com/disintegration/imaging"
	"golang.org/x/image/webp"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

const jpegQuality = 85

// Info describes an image detected from its content.
type Info struct {
	Format domain.Format
	Width  int
	Height int
}

// MaxSide is the largest accepted image width or height in pixels.
const MaxSide = 8000

// Inspect detects the format from the magic bytes, checks the dimensions before decoding
// and then decodes the whole image, rejecting truncated files and data appended after the image.
// Anything other than a valid JPEG, PNG or WebP is domain.ErrInvalidFormat.
func Inspect(data []byte) (Info, error) {
	f, ok := domain.FormatFromMIME(http.DetectContentType(data))
	if !ok {
		return Info{}, domain.ErrInvalidFormat
	}
	cfg, err := decodeConfig(bytes.NewReader(data), f)
	if err != nil {
		return Info{}, fmt.Errorf("%w: %w", domain.ErrInvalidFormat, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxSide || cfg.Height > MaxSide {
		return Info{}, fmt.Errorf("%w: image %dx%d too large, max %dx%d", domain.ErrInvalidFormat, cfg.Width, cfg.Height, MaxSide, MaxSide)
	}
	r := bytes.NewReader(data)
	if _, err := Decode(r, f); err != nil {
		return Info{}, err
	}
	if hasTrailingData(data, r, f) {
		return Info{}, fmt.Errorf("%w: data after the end of the image", domain.ErrInvalidFormat)
	}
	return Info{Format: f, Width: cfg.Width, Height: cfg.Height}, nil
}

func hasTrailingData(data []byte, rest *bytes.Reader, f domain.Format) bool {
	switch f {
	case domain.FormatPNG:
		return rest.Len() > 0
	case domain.FormatWebP:
		return len(data) < 8 || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data))
	}
	return false
}

func decodeConfig(r io.Reader, f domain.Format) (image.Config, error) {
	switch f {
	case domain.FormatJPEG:
		return jpeg.DecodeConfig(r)
	case domain.FormatPNG:
		return png.DecodeConfig(r)
	case domain.FormatWebP:
		return webp.DecodeConfig(r)
	}
	return image.Config{}, fmt.Errorf("unsupported format %q", f)
}

// Decode reads an image of the given format.
func Decode(r io.Reader, f domain.Format) (image.Image, error) {
	var img image.Image
	var err error
	switch f {
	case domain.FormatJPEG:
		img, err = jpeg.Decode(r)
	case domain.FormatPNG:
		img, err = png.Decode(r)
	case domain.FormatWebP:
		img, err = webp.Decode(r)
	default:
		err = fmt.Errorf("unsupported format %q", f)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidFormat, err)
	}
	return img, nil
}

// Thumbnail crops the image to a centered square of the given size; the original size keeps it as is.
func Thumbnail(img image.Image, s domain.Size) image.Image {
	w, h := s.Dimensions()
	if w == 0 {
		return img
	}
	return imaging.Fill(img, w, h, imaging.Center, imaging.Lanczos)
}

// Encode writes the image in the given format; WebP is lossless.
func Encode(w io.Writer, img image.Image, f domain.Format) error {
	switch f {
	case domain.FormatJPEG:
		return jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality})
	case domain.FormatPNG:
		return png.Encode(w, img)
	case domain.FormatWebP:
		return nativewebp.Encode(w, img, nil)
	}
	return fmt.Errorf("unsupported format %q", f)
}
