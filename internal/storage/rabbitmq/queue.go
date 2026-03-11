// Package rabbitmq provides a RabbitMQ-backed implementation of storage.Queue.
//
// Topology:
//   - A single durable topic exchange ("proxyllm.events") is declared once.
//   - Each Subscribe call declares a durable, auto-delete queue named
//     "proxyllm.{topic}.{uuid}" and binds it to the exchange with the topic
//     as routing key.
//   - Publish routes messages to the exchange with the topic as routing key.
//
// Reconnection:
//   - A background goroutine watches for connection/channel errors and
//     reconnects with exponential backoff (up to 30 s).
//   - Subscribers are re-registered automatically after reconnect.
package rabbitmq

import (
	"context"
	"encoding/hex"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const exchange = "proxyllm.events"

type subscription struct {
	topic   string
	queue   string
	handler func(ctx context.Context, payload []byte) error
}

// RabbitMQQueue implements storage.Queue backed by RabbitMQ.
type RabbitMQQueue struct {
	url  string

	mu   sync.RWMutex
	conn *amqp.Connection
	ch   *amqp.Channel

	subs   []*subscription
	subsMu sync.RWMutex

	wg     sync.WaitGroup
	stopCh chan struct{}
	once   sync.Once
}

// New dials RabbitMQ and returns a ready-to-use RabbitMQQueue.
// url format: "amqp://user:pass@host:port/"
func New(url string) (*RabbitMQQueue, error) {
	q := &RabbitMQQueue{
		url:    url,
		stopCh: make(chan struct{}),
	}
	if err := q.connect(); err != nil {
		return nil, fmt.Errorf("rabbitmq: initial connect: %w", err)
	}
	// Watch for connection errors and reconnect automatically.
	q.wg.Add(1)
	go q.watchdog()
	return q, nil
}

// Publish sends payload to the exchange with topic as the routing key.
// Non-blocking from the caller's perspective; delivery is async.
func (q *RabbitMQQueue) Publish(ctx context.Context, topic string, payload []byte) error {
	q.mu.RLock()
	ch := q.ch
	q.mu.RUnlock()
	if ch == nil {
		return fmt.Errorf("rabbitmq: not connected")
	}
	return ch.PublishWithContext(ctx, exchange, topic, false, false,
		amqp.Publishing{
			ContentType:  "application/octet-stream",
			DeliveryMode: amqp.Persistent,
			Body:         payload,
		},
	)
}

// Subscribe registers handler for messages on topic.
// A dedicated goroutine is started internally; this call is non-blocking.
// handler is called sequentially per subscription (one goroutine per sub).
func (q *RabbitMQQueue) Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, payload []byte) error) error {
	queueName, err := q.bindQueue(topic)
	if err != nil {
		return fmt.Errorf("rabbitmq: subscribe bind: %w", err)
	}

	sub := &subscription{topic: topic, queue: queueName, handler: handler}
	q.subsMu.Lock()
	q.subs = append(q.subs, sub)
	q.subsMu.Unlock()

	q.wg.Add(1)
	go q.consume(ctx, sub)
	return nil
}

// Close stops all consumers and closes the AMQP connection.
func (q *RabbitMQQueue) Close() error {
	q.once.Do(func() { close(q.stopCh) })
	q.wg.Wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.conn != nil && !q.conn.IsClosed() {
		return q.conn.Close()
	}
	return nil
}

// ─── internal ────────────────────────────────────────────────────────────────

func (q *RabbitMQQueue) connect() error {
	conn, err := amqp.Dial(q.url)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return err
	}
	// Declare the exchange once; idempotent.
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("declare exchange: %w", err)
	}
	// Prefetch 1 for fair dispatch; handler processes one message at a time.
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("set qos: %w", err)
	}
	q.mu.Lock()
	q.conn = conn
	q.ch = ch
	q.mu.Unlock()
	return nil
}

func (q *RabbitMQQueue) bindQueue(topic string) (string, error) {
	q.mu.RLock()
	ch := q.ch
	q.mu.RUnlock()

	queueName := fmt.Sprintf("proxyllm.%s.%s", topic, shortID())
	_, err := ch.QueueDeclare(queueName, true, true, false, false, nil)
	if err != nil {
		return "", err
	}
	if err := ch.QueueBind(queueName, topic, exchange, false, nil); err != nil {
		return "", err
	}
	return queueName, nil
}

func (q *RabbitMQQueue) consume(ctx context.Context, sub *subscription) {
	defer q.wg.Done()
	for {
		select {
		case <-q.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		q.mu.RLock()
		ch := q.ch
		q.mu.RUnlock()

		if ch == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		deliveries, err := ch.Consume(sub.queue, "", false, false, false, false, nil)
		if err != nil {
			slog.Warn("rabbitmq: consume start failed", "queue", sub.queue, "err", err)
			time.Sleep(2 * time.Second)
			continue
		}

		slog.Info("rabbitmq: consumer started", "queue", sub.queue, "topic", sub.topic)
		for {
			select {
			case <-q.stopCh:
				return
			case <-ctx.Done():
				return
			case msg, ok := <-deliveries:
				if !ok {
					// Channel closed (reconnect in progress)
					goto reconnectWait
				}
				if err := sub.handler(ctx, msg.Body); err != nil {
					slog.Error("rabbitmq: handler error", "topic", sub.topic, "err", err)
					_ = msg.Nack(false, true) // requeue
				} else {
					_ = msg.Ack(false)
				}
			}
		}
	reconnectWait:
		time.Sleep(time.Second)
	}
}

func (q *RabbitMQQueue) watchdog() {
	defer q.wg.Done()
	for {
		q.mu.RLock()
		conn := q.conn
		q.mu.RUnlock()

		closeCh := make(chan *amqp.Error, 1)
		if conn != nil {
			conn.NotifyClose(closeCh)
		}

		select {
		case <-q.stopCh:
			return
		case amqpErr, ok := <-closeCh:
			if !ok {
				return
			}
			slog.Warn("rabbitmq: connection closed, reconnecting", "err", amqpErr)
		}

		// Exponential backoff reconnect.
		backoff := time.Second
		for {
			select {
			case <-q.stopCh:
				return
			case <-time.After(backoff):
			}
			if err := q.connect(); err != nil {
				slog.Warn("rabbitmq: reconnect failed", "err", err, "backoff", backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			slog.Info("rabbitmq: reconnected")
			// Re-bind queues for all active subscriptions.
			q.subsMu.RLock()
			subs := make([]*subscription, len(q.subs))
			copy(subs, q.subs)
			q.subsMu.RUnlock()
			for _, sub := range subs {
				if name, err := q.bindQueue(sub.topic); err != nil {
					slog.Error("rabbitmq: re-bind queue", "topic", sub.topic, "err", err)
				} else {
					sub.queue = name
				}
			}
			break
		}
	}
}

func shortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
