package domain

import "errors"

// Domain errors mapped to API responses.
var (
	ErrNotFound      = errors.New("avatar not found")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidFormat = errors.New("invalid file format")
	ErrTooLarge      = errors.New("file too large")
	ErrInvalidParam  = errors.New("invalid parameter")
)
