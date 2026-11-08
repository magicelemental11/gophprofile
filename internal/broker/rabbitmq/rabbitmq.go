// Package rabbitmq publishes and consumes avatar events through RabbitMQ.
package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/magicelemental11/gophprofile/internal/domain"
)

// Topology names.
const (
	Exchange        = "avatars.exchange"
	DeadLetter      = "avatars.dlx"
	QueueProcessing = "avatars.processing"
	QueueDeletion   = "avatars.deletion"
	QueueDead       = "avatars.dlq"
)

const confirmTimeout = 5 * time.Second

// Message is a delivered event.
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

// Close closes the connection.
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

// Publish sends event as persistent JSON with a fresh message id and waits for the broker confirm.
func (b *Broker) Publish(ctx context.Context, routingKey string, event any) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	msg := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    uuid.NewString(),
		Timestamp:    time.Now(),
		Body:         body,
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
// flight when ctx is canceled are finished before Consume returns.
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
				m := Message{ID: d.MessageId, RoutingKey: d.RoutingKey, Body: d.Body}
				if err := h(handlerCtx, m); err != nil {
					_ = d.Nack(false, false)
					continue
				}
				_ = d.Ack(false)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() == nil {
		return fmt.Errorf("consume %s: delivery channel closed", queue)
	}
	return nil
}
