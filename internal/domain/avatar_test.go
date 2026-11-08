package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]Size{"": SizeOriginal, "original": SizeOriginal, "100x100": Size100, "300x300": Size300} {
		got, err := ParseSize(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
	}
	_, err := ParseSize("50x50")
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestSizeDimensions(t *testing.T) {
	w, h := Size300.Dimensions()
	assert.Equal(t, 300, w)
	assert.Equal(t, 300, h)
	w, h = SizeOriginal.Dimensions()
	assert.Zero(t, w+h)
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": "", "jpeg": FormatJPEG, "jpg": FormatJPEG, "png": FormatPNG, "webp": FormatWebP} {
		got, err := ParseFormat(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
	}
	_, err := ParseFormat("gif")
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestFormatMIME(t *testing.T) {
	assert.Equal(t, "image/jpeg", FormatJPEG.MIME())
	assert.Equal(t, "image/png", FormatPNG.MIME())
	assert.Equal(t, "image/webp", FormatWebP.MIME())
	f, ok := FormatFromMIME("image/png")
	assert.True(t, ok)
	assert.Equal(t, FormatPNG, f)
	_, ok = FormatFromMIME("image/gif")
	assert.False(t, ok)
}

func TestAvatarStatus(t *testing.T) {
	tests := []struct {
		a    Avatar
		want string
	}{
		{Avatar{UploadStatus: UploadUploading, ProcessingStatus: ProcessingPending}, StatusProcessing},
		{Avatar{UploadStatus: UploadUploaded, ProcessingStatus: ProcessingProcessing}, StatusProcessing},
		{Avatar{UploadStatus: UploadUploaded, ProcessingStatus: ProcessingCompleted}, StatusReady},
		{Avatar{UploadStatus: UploadUploaded, ProcessingStatus: ProcessingFailed}, StatusFailed},
		{Avatar{UploadStatus: UploadFailed, ProcessingStatus: ProcessingPending}, StatusFailed},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.a.Status())
	}
}

func TestKeys(t *testing.T) {
	assert.Equal(t, "originals/abc.png", OriginalKey("abc", FormatPNG))
	assert.Equal(t, "thumbnails/abc/100x100.jpg", ThumbnailKey("abc", Size100))

	a := Avatar{ID: "abc", S3Key: "originals/abc.png"}
	assert.Equal(t, []string{"originals/abc.png", "thumbnails/abc/100x100.jpg", "thumbnails/abc/300x300.jpg"}, a.AllKeys())
}

func TestThumbnailKeysOf(t *testing.T) {
	assert.Equal(t, []string{"thumbnails/a1/100x100.jpg", "thumbnails/a1/300x300.jpg"}, ThumbnailKeysOf("a1"))
	a := Avatar{ID: "a1", S3Key: "originals/a1.png"}
	assert.Equal(t, append([]string{"originals/a1.png"}, ThumbnailKeysOf("a1")...), a.AllKeys())
}

func TestOwnsKey(t *testing.T) {
	a := Avatar{ID: "a1", S3Key: OriginalKey("a1", FormatWebP)}
	for _, k := range a.AllKeys() {
		assert.True(t, OwnsKey("a1", k), k)
	}
	for _, k := range []string{"originals/a2.png", "originals/a1.gif", "originals/a1/../a2.png", "thumbnails/a1/50x50.jpg", "thumbnails/a2/100x100.jpg", ""} {
		assert.False(t, OwnsKey("a1", k), k)
	}
}
