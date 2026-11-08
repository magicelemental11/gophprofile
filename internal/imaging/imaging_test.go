package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/HugoSmits86/nativewebp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func sample(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	return img
}

func encoded(t *testing.T, f string, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := sample(w, h)
	var err error
	switch f {
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "png":
		err = png.Encode(&buf, img)
	case "webp":
		err = nativewebp.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	require.NoError(t, err)
	return buf.Bytes()
}

func TestInspect(t *testing.T) {
	for _, f := range []domain.Format{domain.FormatJPEG, domain.FormatPNG, domain.FormatWebP} {
		info, err := Inspect(encoded(t, string(f), 64, 48))
		require.NoError(t, err, f)
		assert.Equal(t, Info{Format: f, Width: 64, Height: 48}, info)
	}
}

func TestInspectRejects(t *testing.T) {
	png := encoded(t, "png", 10, 10)
	for name, data := range map[string][]byte{
		"gif":       encoded(t, "gif", 10, 10),
		"text":      []byte("hello, i am not an image"),
		"empty":     nil,
		"truncated": png[:20],
	} {
		_, err := Inspect(data)
		assert.ErrorIs(t, err, domain.ErrInvalidFormat, name)
	}
}

func TestDecodeThumbnailEncode(t *testing.T) {
	for _, src := range []domain.Format{domain.FormatJPEG, domain.FormatPNG, domain.FormatWebP} {
		img, err := Decode(bytes.NewReader(encoded(t, string(src), 120, 80)), src)
		require.NoError(t, err, src)

		thumb := Thumbnail(img, domain.Size100)
		assert.Equal(t, image.Rect(0, 0, 100, 100), thumb.Bounds())

		for _, dst := range []domain.Format{domain.FormatJPEG, domain.FormatPNG, domain.FormatWebP} {
			var buf bytes.Buffer
			require.NoError(t, Encode(&buf, thumb, dst))
			info, err := Inspect(buf.Bytes())
			require.NoError(t, err, "%s -> %s", src, dst)
			assert.Equal(t, Info{Format: dst, Width: 100, Height: 100}, info)
		}
	}
}

func TestThumbnailOriginalKeepsImage(t *testing.T) {
	img := sample(30, 20)
	assert.Equal(t, img, Thumbnail(img, domain.SizeOriginal))
}

func TestDecodeErrors(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte("garbage")), domain.FormatPNG)
	assert.ErrorIs(t, err, domain.ErrInvalidFormat)
	_, err = Decode(bytes.NewReader(nil), "bmp")
	assert.ErrorIs(t, err, domain.ErrInvalidFormat)
	assert.Error(t, Encode(&bytes.Buffer{}, sample(1, 1), "bmp"))
}
