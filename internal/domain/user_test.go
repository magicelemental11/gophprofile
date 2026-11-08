package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidUserID(t *testing.T) {
	for _, id := range []string{"gopher", "gopher@example.com", "a.b_c%d+e-f", strings.Repeat("x", 255)} {
		assert.True(t, ValidUserID(id), id)
	}
	for _, id := range []string{"", " ", "a b", "a/b", "<script>", "юзер", "a\nb", strings.Repeat("x", 256)} {
		assert.False(t, ValidUserID(id), id)
	}
}

func TestCleanFileName(t *testing.T) {
	for in, want := range map[string]string{
		"cat.png":                "cat.png",
		"../../etc/passwd":       "passwd",
		`C:\Users\me\cat.png`:    "cat.png",
		"":                       "",
		"/":                      "",
		"..":                     "",
		"dir/":                   "dir",
		"bad\xffname.png":        "badname.png",
		"tab\tand\x00nul.png":    "tabandnul.png",
		strings.Repeat("я", 300): strings.Repeat("я", 255),
	} {
		assert.Equal(t, want, CleanFileName(in), in)
	}
}
