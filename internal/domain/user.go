package domain

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// MaxUserIDLength and MaxFileNameLength match the column sizes in the database.
const (
	MaxUserIDLength   = 255
	MaxFileNameLength = 255
)

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9._%+@-]+$`)

// ValidUserID reports whether id is a non-empty user identifier of up to 255 safe characters.
func ValidUserID(id string) bool {
	return len(id) <= MaxUserIDLength && userIDPattern.MatchString(id)
}

// CleanFileName drops directories, control characters and invalid UTF-8 from a client file name
// and cuts it to MaxFileNameLength characters.
func CleanFileName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	if r := []rune(name); len(r) > MaxFileNameLength {
		name = string(r[:MaxFileNameLength])
	}
	return name
}
