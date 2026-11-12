package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewBadDSN(t *testing.T) {
	_, err := New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	assert.Error(t, err)
	_, err = New(context.Background(), "::bad::")
	assert.Error(t, err)
}
