package s3

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/config"
	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/telemetry/telemetrytest"
)

func fakeS3(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Has("location"):
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`))
		case r.Method == http.MethodHead && strings.Trim(r.URL.Path, "/") == "avatars":
		case r.Method == http.MethodPut:
			w.Header().Set("ETag", `"abc"`)
		default:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newFake(t *testing.T) *Storage {
	t.Helper()
	srv := fakeS3(t)
	s, err := New(context.Background(), config.S3{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "k",
		SecretKey: "s",
		Bucket:    "avatars",
	})
	require.NoError(t, err)
	return s
}

func TestPutSpan(t *testing.T) {
	s := newFake(t)
	rec := telemetrytest.Record(t)
	ctx, parent := otel.Tracer("test").Start(context.Background(), "upload_avatar")

	etag, err := s.Put(ctx, "originals/a.png", bytes.NewReader([]byte("img")), 3, "image/png")
	parent.End()

	require.NoError(t, err)
	assert.Equal(t, "abc", etag)
	put := telemetrytest.Ended(t, rec, "s3.put_object")
	assert.Equal(t, parent.SpanContext().SpanID(), put.Parent().SpanID())
	attrs := telemetrytest.Attrs(put)
	assert.Equal(t, "avatars", attrs["aws.s3.bucket"].AsString())
	assert.Equal(t, "originals/a.png", attrs["aws.s3.key"].AsString())
	assert.Equal(t, int64(3), attrs["file_size"].AsInt64())

	httpSpan := telemetrytest.Ended(t, rec, "HTTP PUT")
	assert.Equal(t, trace.SpanKindClient, httpSpan.SpanKind())
	assert.Equal(t, put.SpanContext().SpanID(), httpSpan.Parent().SpanID())
}

func TestGetMissingIsNotSpanError(t *testing.T) {
	s := newFake(t)
	rec := telemetrytest.Record(t)
	ctx, parent := otel.Tracer("test").Start(context.Background(), "read")

	_, err := s.Get(ctx, "originals/none.png")
	parent.End()

	require.ErrorIs(t, err, domain.ErrNotFound)
	get := telemetrytest.Ended(t, rec, "s3.get_object")
	assert.Equal(t, codes.Unset, get.Status().Code)
}

func TestNoSpansWithoutParent(t *testing.T) {
	s := newFake(t)
	rec := telemetrytest.Record(t)

	require.NoError(t, s.Ping(context.Background()))

	assert.Empty(t, rec.Ended())
}
