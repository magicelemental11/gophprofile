package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

func TestUploadOutcomes(t *testing.T) {
	e := newEnv()
	e.upload(t, "u")
	_, err := e.svc.Upload(context.Background(), "u", "x.txt", strings.NewReader("not an image"))
	require.Error(t, err)
	e.store.putFails = 1
	_, err = e.svc.Upload(context.Background(), "u", "a.png", bytes.NewReader(pngBytes(t, 4, 4)))
	require.Error(t, err)

	assert.Equal(t, []domain.Outcome{domain.OutcomeSuccess, domain.OutcomeRejected, domain.OutcomeError}, e.metrics.uploads)
}

func TestDeleteOutcomes(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")

	require.NoError(t, e.svc.Delete(context.Background(), "u", a.ID))
	require.ErrorIs(t, e.svc.DeleteCurrent(context.Background(), "u", "other"), domain.ErrForbidden)

	assert.Equal(t, []domain.Outcome{domain.OutcomeSuccess, domain.OutcomeRejected}, e.metrics.deletes)
}
