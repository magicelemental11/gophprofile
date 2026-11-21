// Package rabbitmq publishes and consumes avatar events through RabbitMQ.
package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/magicelemental11/gophprofile/internal/domain"
	"github.com/magicelemental11/gophprofile/internal/telemetry"
)

// Exchanges and queues declared by Dial; failed messages end up in QueueDead.
const (
	Exchange        = "avatars.exchange"
	DeadLetter      = "avatars.dlx"
	QueueProcessing = "avatars.processing"
	QueueDeletion   = "avatars.deletion"
	QueueDead       = "avatars.dlq"
)

const confirmTimeout = 5 * time.Second

// Message is a delivered event; ID is the message id set by Publish and used for deduplication.
type Message struct {
	ID         string
	RoutingKey string
	Body       []byte
}

// Handler processes one message; an error sends it to the dead-letter queue.
type Handler func(ctx context.Context, m Message) error

// Broker holds one connection and a confirming publishing channel.
type Broker struct {
	conn *amqp.Connection
	mu   sync.Mutex
	pub  *amqp.Channel
}

// Dial connects to RabbitMQ and declares the topology.
func Dial(url string) (*Broker, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	if err := declareTopology(ch); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("enable confirms: %w", err)
	}
	return &Broker{conn: conn, pub: ch}, nil
}

func declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(Exchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}
	if err := ch.ExchangeDeclare(DeadLetter, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead-letter exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(QueueDead, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare %s: %w", QueueDead, err)
	}
	if err := ch.QueueBind(QueueDead, "", DeadLetter, false, nil); err != nil {
		return fmt.Errorf("bind %s: %w", QueueDead, err)
	}
	bindings := map[string][]string{
		QueueProcessing: {domain.RoutingUploaded, domain.RoutingProcess},
		QueueDeletion:   {domain.RoutingDeleted},
	}
	args := amqp.Table{"x-dead-letter-exchange": DeadLetter}
	for queue, keys := range bindings {
		if _, err := ch.QueueDeclare(queue, true, false, false, false, args); err != nil {
			return fmt.Errorf("declare %s: %w", queue, err)
		}
		for _, key := range keys {
			if err := ch.QueueBind(queue, key, Exchange, false, nil); err != nil {
				return fmt.Errorf("bind %s to %s: %w", queue, key, err)
			}
		}
	}
	return nil
}

// Close closes the connection together with all channels and consumers on it.
func (b *Broker) Close() error {
	return b.conn.Close()
}

// Ping reports whether the connection is alive.
func (b *Broker) Ping(context.Context) error {
	if b.conn.IsClosed() {
		return errors.New("connection closed")
	}
	return nil
}

// NotifyClose returns a channel that receives an error when the connection drops.
func (b *Broker) NotifyClose() <-chan *amqp.Error {
	return b.conn.NotifyClose(make(chan *amqp.Error, 1))
}

// Publish sends ev as a persistent JSON message with ev.ID as the message id and waits for the broker confirm.
// The trace context of ctx travels in the message headers.
func (b *Broker) Publish(ctx context.Context, ev domain.Event) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "publish "+Exchange,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(append(messageAttrs(ev.ID, ev.RoutingKey),
			semconv.MessagingDestinationName(Exchange),
			semconv.MessagingOperationTypeSend,
		)...))
	err := b.publish(ctx, ev)
	telemetry.End(span, err, nil)
	return err
}

func (b *Broker) publish(ctx context.Context, ev domain.Event) error {
	routingKey := ev.RoutingKey
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(headers))
	msg := amqp.Publishing{
		Headers:      headers,
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    ev.ID,
		Timestamp:    time.Now(),
		Body:         ev.Payload,
	}

	b.mu.Lock()
	dc, err := b.pub.PublishWithDeferredConfirmWithContext(ctx, Exchange, routingKey, false, false, msg)
	b.mu.Unlock()
	if err != nil {
		return fmt.Errorf("publish %s: %w", routingKey, err)
	}
	ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("confirm %s: %w", routingKey, err)
	}
	if !acked {
		return fmt.Errorf("publish %s: broker rejected the message", routingKey)
	}
	return nil
}

// Consume handles messages from queue with up to prefetch concurrent handlers until ctx is done.
// Handled messages are acked, failed ones are rejected to the dead-letter queue. Messages in
// flight when ctx is canceled are finished before Consume returns. Each message is handled in a
// span continuing the trace from its headers.
func (b *Broker) Consume(ctx context.Context, queue string, prefetch int, h Handler) error {
	ch, err := b.conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}
	tag := queue + "-" + uuid.NewString()
	deliveries, err := ch.Consume(queue, tag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queue, err)
	}

	go func() {
		<-ctx.Done()
		_ = ch.Cancel(tag, false)
	}()

	handlerCtx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	for range prefetch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range deliveries {
				handle(handlerCtx, queue, d, h)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() == nil {
		return fmt.Errorf("consume %s: delivery channel closed", queue)
	}
	return nil
}

func handle(ctx context.Context, queue string, d amqp.Delivery, h Handler) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, headerCarrier(d.Headers))
	ctx, span := otel.Tracer(tracerName).Start(ctx, "process "+queue,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(append(messageAttrs(d.MessageId, d.RoutingKey),
			semconv.MessagingDestinationName(queue),
			semconv.MessagingOperationTypeProcess,
		)...))
	err := h(ctx, Message{ID: d.MessageId, RoutingKey: d.RoutingKey, Body: d.Body})
	if err != nil {
		span.SetAttributes(attribute.Bool("messaging.rabbitmq.dead_lettered", true))
		_ = d.Nack(false, false)
	} else {
		_ = d.Ack(false)
	}
	telemetry.End(span, err, nil)
}

const tracerName = "github.com/magicelemental11/gophprofile/internal/broker/rabbitmq"

func messageAttrs(id, routingKey string) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.MessagingSystemRabbitMQ,
		semconv.MessagingMessageID(id),
		semconv.MessagingRabbitMQDestinationRoutingKey(routingKey),
	}
}

type headerCarrier amqp.Table

func (c headerCarrier) Get(key string) string {
	v, _ := c[key].(string)
	return v
}

func (c headerCarrier) Set(key, value string) { c[key] = value }

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
