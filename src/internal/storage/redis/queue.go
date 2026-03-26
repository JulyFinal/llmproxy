package redis

import (
	"context"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisQueue implements storage.Queue using Redis lists (RPUSH / BLPOP).
//
// Key schema:  queue:{topic}
//
// Delivery semantics:
//   - At-least-once: if handler returns an error the payload is RPUSH'd back
//     to the tail of the list (best-effort requeue).
//   - Messages survive consumer restarts (persisted in Redis).
//   - No fan-out: competing consumers share a single list (work-queue pattern).
//     This is sufficient for our logging use-case where one writer drains the
//     queue; add separate topic keys if fan-out is ever needed.
type RedisQueue struct {
	client *goredis.Client
	stopCh chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

// NewQueue creates a RedisQueue using the given connection parameters.
func NewQueue(addr, password string, db int) *RedisQueue {
	client := goredis.NewClient(&goredis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  2 * time.Second, // keep short so BLPOP can be interrupted
		WriteTimeout: 3 * time.Second,
		PoolSize:     5,
		MinIdleConns: 1,
	})
	return &RedisQueue{
		client: client,
		stopCh: make(chan struct{}),
	}
}

// Ping checks connectivity. Call once at startup.
func (q *RedisQueue) Ping(ctx context.Context) error {
	return q.client.Ping(ctx).Err()
}

// Publish appends payload to the right end of the Redis list for topic.
// Non-blocking from the caller's perspective.
func (q *RedisQueue) Publish(ctx context.Context, topic string, payload []byte) error {
	return q.client.RPush(ctx, queueKey(topic), payload).Err()
}

// Subscribe starts a goroutine that calls handler for each message on topic.
// Non-blocking: returns immediately. handler is called sequentially (one message
// at a time per Subscribe call). Add multiple Subscribe calls for concurrency.
func (q *RedisQueue) Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, payload []byte) error) error {
	q.wg.Add(1)
	go q.consume(ctx, topic, handler)
	return nil
}

// Close stops all consumer goroutines and waits for them to finish.
func (q *RedisQueue) Close() error {
	q.once.Do(func() { close(q.stopCh) })
	q.wg.Wait()
	return q.client.Close()
}

// ─── internal ─────────────────────────────────────────────────────────────────

func (q *RedisQueue) consume(ctx context.Context, topic string, handler func(ctx context.Context, payload []byte) error) {
	defer q.wg.Done()
	key := queueKey(topic)

	for {
		// Check stop signal before blocking.
		select {
		case <-q.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		// BLPOP blocks for up to 1 s so we can poll stopCh regularly.
		// It returns [key, value] on success, goredis.Nil on timeout.
		result, err := q.client.BLPop(ctx, time.Second, key).Result()
		if err == goredis.Nil {
			continue // timeout — loop back and re-check stop signal
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("redis queue: blpop error", "topic", topic, "err", err)
			// Back off briefly to avoid tight error loops on connection issues.
			select {
			case <-q.stopCh:
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		payload := []byte(result[1]) // result[0] = key, result[1] = value

		if err := handler(ctx, payload); err != nil {
			slog.Error("redis queue: handler error, requeueing", "topic", topic, "err", err)
			// Best-effort requeue: push to tail so other messages aren't blocked.
			_ = q.client.RPush(context.Background(), key, payload).Err()
		}
	}
}

func queueKey(topic string) string {
	return "queue:" + topic
}
