package memory

import (
	"context"
	"encoding/binary"
	"sync"
	"time"
)

type cacheEntry struct {
	value    []byte
	expireAt time.Time // zero = no expiry
}

func (e *cacheEntry) expired(now time.Time) bool {
	return !e.expireAt.IsZero() && now.After(e.expireAt)
}

// MemoryCache is an in-memory implementation of storage.Cache.
// It is safe for concurrent use by multiple goroutines.
type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	done    chan struct{}
	wg      sync.WaitGroup
}

// NewMemoryCache returns a started MemoryCache. Call Close when done.
func NewMemoryCache() *MemoryCache {
	c := &MemoryCache{
		entries: make(map[string]*cacheEntry),
		done:    make(chan struct{}),
	}
	c.wg.Add(1)
	go c.cleanup()
	return c
}

// cleanup runs every 10 seconds and removes expired keys.
func (c *MemoryCache) cleanup() {
	defer c.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			c.mu.Lock()
			for k, e := range c.entries {
				if e.expired(now) {
					delete(c.entries, k)
				}
			}
			c.mu.Unlock()
		case <-c.done:
			return
		}
	}
}

// Get returns the value stored under key, and whether it was found and
// not yet expired.
func (c *MemoryCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || e.expired(time.Now()) {
		return nil, false
	}
	// Return a defensive copy so callers cannot mutate internal state.
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true
}

// Set stores value under key with the given TTL. A zero TTL means no expiry.
func (c *MemoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	entry := &cacheEntry{
		value: make([]byte, len(value)),
	}
	copy(entry.value, value)
	if ttl > 0 {
		entry.expireAt = time.Now().Add(ttl)
	}
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
	return nil
}

// Delete removes key from the cache.
func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	return nil
}

// IncrBy atomically increments the int64 value stored at key by delta and
// returns the new value. If the key does not exist it is created with
// value = delta and the given TTL applied. Subsequent calls do NOT reset TTL.
// The value is stored as an 8-byte big-endian integer.
func (c *MemoryCache) IncrBy(_ context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	e, ok := c.entries[key]
	if !ok || e.expired(now) {
		// Create a fresh entry.
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(delta))
		newEntry := &cacheEntry{value: buf}
		if ttl > 0 {
			newEntry.expireAt = now.Add(ttl)
		}
		c.entries[key] = newEntry
		return delta, nil
	}

	// Decode existing value.
	var current int64
	if len(e.value) == 8 {
		current = int64(binary.BigEndian.Uint64(e.value))
	}
	current += delta
	binary.BigEndian.PutUint64(e.value, uint64(current))
	return current, nil
}

// Close stops the background cleanup goroutine and waits for it to exit.
func (c *MemoryCache) Close() error {
	close(c.done)
	c.wg.Wait()
	return nil
}
