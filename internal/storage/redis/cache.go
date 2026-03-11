// Package redis provides a Redis-backed implementation of storage.Cache.
// It is a drop-in replacement for the in-memory cache and is suitable for
// distributed deployments where multiple proxy instances share rate-limit state.
package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisCache implements storage.Cache using Redis.
type RedisCache struct {
	client *goredis.Client

	// Lua script: INCRBY key delta, then set TTL only if the key has no expiry
	// (i.e. was just created). This preserves the "TTL set on creation only"
	// contract required by the rate-limiter sliding window.
	incrScript *goredis.Script
}

const incrLua = `
local val = redis.call('INCRBY', KEYS[1], ARGV[1])
local ttl = redis.call('TTL', KEYS[1])
if ttl == -1 then
    redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return val
`

// New creates a RedisCache connected to the given address.
//   - addr:     "host:port"
//   - password: empty string for no auth
//   - db:       Redis database index (usually 0)
func New(addr, password string, db int) *RedisCache {
	client := goredis.NewClient(&goredis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
		MinIdleConns: 2,
	})
	return &RedisCache{
		client:     client,
		incrScript: goredis.NewScript(incrLua),
	}
}

// Ping checks that Redis is reachable. Call once at startup.
func (c *RedisCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := c.client.Get(ctx, key).Bytes()
	if err == goredis.Nil {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return val, true
}

func (c *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *RedisCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

// IncrBy atomically increments key by delta.
// TTL is set via PEXPIRE only when the key has no existing expiry (just created).
// Subsequent increments within the same window leave the TTL untouched.
func (c *RedisCache) IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	ttlMs := ttl.Milliseconds()
	result, err := c.incrScript.Run(ctx, c.client, []string{key}, delta, ttlMs).Int64()
	return result, err
}

func (c *RedisCache) Close() error {
	return c.client.Close()
}

// DebugTTL returns the remaining TTL of a key. Used in tests only.
func DebugTTL(ctx context.Context, c *RedisCache, key string) (time.Duration, error) {
	return c.client.TTL(ctx, key).Result()
}
