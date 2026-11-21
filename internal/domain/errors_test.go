package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOutcomeOf(t *testing.T) {
	assert.Equal(t, OutcomeSuccess, OutcomeOf(nil))
	for _, err := range []error{ErrNotFound, ErrForbidden, ErrInvalidFormat, ErrTooLarge, ErrInvalidParam, fmt.Errorf("get: %w", ErrNotFound)} {
		assert.True(t, IsClientError(err), err)
		assert.Equal(t, OutcomeRejected, OutcomeOf(err), err)
	}
	assert.False(t, IsClientError(errors.New("db down")))
	assert.Equal(t, OutcomeError, OutcomeOf(errors.New("db down")))
}
