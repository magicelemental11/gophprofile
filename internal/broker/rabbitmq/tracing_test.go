package rabbitmq

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/telemetry/telemetrytest"
)

func TestHeaderCarrierRoundTrip(t *testing.T) {
	telemetrytest.Record(t)
	ctx, span := otel.Tracer("test").Start(context.Background(), "publish")
	defer span.End()

	headers := amqp.Table{"x-other": int32(1)}
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(headers))
	got := trace.SpanContextFromContext(otel.GetTextMapPropagator().Extract(context.Background(), headerCarrier(headers)))

	assert.Equal(t, span.SpanContext().TraceID(), got.TraceID())
	assert.Equal(t, span.SpanContext().SpanID(), got.SpanID())
	assert.Contains(t, headerCarrier(headers).Keys(), "traceparent")
	assert.Empty(t, headerCarrier(headers).Get("x-other"), "non-string headers are ignored")
	assert.Empty(t, headerCarrier(nil).Get("traceparent"))
}
