package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/magicelemental11/gophprofile/internal/telemetry/telemetrytest"
)

func TestUploadSpan(t *testing.T) {
	rec := telemetrytest.Record(t)
	e := newEnv()
	ctx, parent := otel.Tracer("test").Start(context.Background(), "POST /api/v1/avatars")
	data := pngBytes(t, 20, 10)

	a, err := e.svc.Upload(ctx, "gopher@example.com", "cat.png", bytes.NewReader(data))
	require.NoError(t, err)
	parent.End()

	s := telemetrytest.Ended(t, rec, "upload_avatar")
	assert.Equal(t, parent.SpanContext().SpanID(), s.Parent().SpanID())
	assert.Equal(t, codes.Unset, s.Status().Code)
	got := telemetrytest.Attrs(s)
	assert.Equal(t, "gopher@example.com", got["user_id"].AsString())
	assert.Equal(t, "cat.png", got["file_name"].AsString())
	assert.Equal(t, int64(len(data)), got["file_size"].AsInt64())
	assert.Equal(t, "image/png", got["mime_type"].AsString())
	assert.Equal(t, a.ID, got["avatar_id"].AsString())
}

func TestUploadSpanErrors(t *testing.T) {
	rec := telemetrytest.Record(t)
	e := newEnv()

	_, err := e.svc.Upload(context.Background(), "u", "x.txt", strings.NewReader("not an image"))
	require.Error(t, err)
	e.store.putFails = 1
	_, err = e.svc.Upload(context.Background(), "u", "cat.png", bytes.NewReader(pngBytes(t, 4, 4)))
	require.Error(t, err)

	ended := rec.Ended()
	require.Len(t, ended, 2)
	assert.Equal(t, codes.Unset, ended[0].Status().Code, "invalid format is the client's fault")
	assert.Equal(t, "client", telemetrytest.Attrs(ended[0])["error.kind"].AsString())
	assert.Equal(t, codes.Error, ended[1].Status().Code)
}

func TestDeleteSpan(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	rec := telemetrytest.Record(t)

	require.NoError(t, e.svc.Delete(context.Background(), "u", a.ID))

	s := telemetrytest.Ended(t, rec, "delete_avatar")
	assert.Equal(t, a.ID, telemetrytest.Attrs(s)["avatar_id"].AsString())
	assert.Equal(t, "u", telemetrytest.Attrs(s)["user_id"].AsString())
}

func TestProcessSpan(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	rec := telemetrytest.Record(t)
	msg := uuid.NewString()

	require.NoError(t, newProcessor(e).Process(context.Background(), msg, processEvent(a.ID)))

	s := telemetrytest.Ended(t, rec, "process_avatar")
	assert.Equal(t, a.ID, telemetrytest.Attrs(s)["avatar_id"].AsString())
	assert.Equal(t, msg, telemetrytest.Attrs(s)["messaging.message.id"].AsString())
}

func TestDuplicateMessageSpanIsNotAnError(t *testing.T) {
	e := newEnv()
	a := e.upload(t, "u")
	msg := uuid.NewString()
	p := newProcessor(e)
	require.NoError(t, p.Process(context.Background(), msg, processEvent(a.ID)))
	rec := telemetrytest.Record(t)

	require.ErrorIs(t, p.Process(context.Background(), msg, processEvent(a.ID)), ErrDuplicate)

	s := telemetrytest.Ended(t, rec, "process_avatar")
	assert.Equal(t, codes.Unset, s.Status().Code)
	assert.True(t, telemetrytest.Attrs(s)["messaging.duplicate"].AsBool())
}
