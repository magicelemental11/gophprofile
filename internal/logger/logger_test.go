package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNew(t *testing.T) {
	l, err := New("debug")
	require.NoError(t, err)
	assert.True(t, l.Core().Enabled(zap.DebugLevel))

	_, err = New("loud")
	assert.Error(t, err)
}
