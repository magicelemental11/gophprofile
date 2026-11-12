package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
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

func pngHeader(w, h uint32) []byte {
	b := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	b = binary.BigEndian.AppendUint32(b, w)
	b = binary.BigEndian.AppendUint32(b, h)
	b = append(b, 8, 6, 0, 0, 0)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[12:]))
}

func TestInspectRejectsHugeDimensions(t *testing.T) {
	for _, dims := range [][2]uint32{{MaxSide + 1, 10}, {10, MaxSide + 1}, {100000, 100000}} {
		_, err := Inspect(pngHeader(dims[0], dims[1]))
		assert.ErrorIs(t, err, domain.ErrInvalidFormat, dims)
		assert.ErrorContains(t, err, "too large", dims)
	}
}

func TestInspectRejectsBrokenBody(t *testing.T) {
	for _, f := range []string{"jpeg", "png", "webp"} {
		data := encoded(t, f, 200, 200)
		_, err := Inspect(data[:len(data)/2])
		assert.ErrorIs(t, err, domain.ErrInvalidFormat, "truncated %s", f)
	}
}

func TestInspectRejectsTrailingData(t *testing.T) {
	zip := []byte("PK\x03\x04 polyglot payload <script>alert(1)</script>")
	for _, f := range []string{"png", "webp"} {
		data := append(encoded(t, f, 20, 20), zip...)
		_, err := Inspect(data)
		assert.ErrorIs(t, err, domain.ErrInvalidFormat, f)
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
