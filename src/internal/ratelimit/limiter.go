// Package ratelimit implements the three-layer rate limiter:
// global → per-model → per-key.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/storage"
)

type Limiter struct {
	mu          sync.RWMutex
	cache       storage.Cache
	global      domain.RateLimitConfig
	modelLimits map[string]domain.RateLimitConfig
	keyLimits   func(keyID string) *domain.RateLimitConfig
	loggedLimits sync.Map // track which models we already logged
}

func New(
	cache storage.Cache,
	global domain.RateLimitConfig,
	modelLimits map[string]domain.RateLimitConfig,
	keyLimits func(keyID string) *domain.RateLimitConfig,
) *Limiter {
	if modelLimits == nil {
		modelLimits = make(map[string]domain.RateLimitConfig)
	}
	return &Limiter{
		cache:       cache,
		global:      global,
		modelLimits: modelLimits,
		keyLimits:   keyLimits,
	}
}

func (l *Limiter) UpdateModelLimits(limits map[string]domain.RateLimitConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.modelLimits = limits
}

// AllowRequest checks RPM and TPM budgets. Now returns status details.
func (l *Limiter) AllowRequest(ctx context.Context, keyID, modelAlias string) (bool, domain.RateLimitStatus, error) {
	status := domain.RateLimitStatus{}

	// Fast path: if all limits are zero (unlimited), skip all checks.
	gRPM, gTPM := l.global.RPM, l.global.TPM
	mRPM, mTPM := l.modelRPM(modelAlias), l.modelTPM(modelAlias)
	kRPM, kTPM := l.keyRPM(keyID), l.keyTPM(keyID)
	if gRPM <= 0 && gTPM <= 0 && mRPM <= 0 && mTPM <= 0 && kRPM <= 0 && kTPM <= 0 {
		return true, status, nil
	}
	// Log which limit is active (helps debug unexpected blocks)
	l.logActiveLimit(keyID, modelAlias, gRPM, gTPM, mRPM, mTPM, kRPM, kTPM)

	now := time.Now()
	ts := now.Unix() / 60
	prevTs := ts - 1
	weight := float64(now.Second()) / 60.0

	// 1. Check RPM
	rpmLayers := []struct {
		scope string
		id    string
		limit int
	}{
		{"global", "", l.global.RPM},
		{"model", modelAlias, l.modelRPM(modelAlias)},
		{"key", keyID, l.keyRPM(keyID)},
	}

	var incremented []string
	for _, layer := range rpmLayers {
		if layer.limit <= 0 {
			continue
		}

		currKey := rpmKey(layer.scope, layer.id, ts)
		prevKey := rpmKey(layer.scope, layer.id, prevTs)

		prevCount := l.getCount(ctx, prevKey)
		currCount, err := l.cache.IncrBy(ctx, currKey, 1, 61*time.Second)
		if err != nil {
			l.undo(ctx, incremented)
			status.BlockReason = "cache_error"
			return false, status, fmt.Errorf("ratelimit: rpm cache error: %w", err)
		}
		incremented = append(incremented, currKey)

		estimated := float64(currCount) + float64(prevCount)*(1.0-weight)
		if estimated > float64(layer.limit) {
			l.undo(ctx, incremented)
			status.BlockReason = "limit_exceeded"
			status.RPMCurrent = int(estimated)
			status.RPMLimit = layer.limit
			return false, status, nil
		}
	}

	// 2. Check TPM
	tpmLayers := []struct {
		scope string
		id    string
		limit int
	}{
		{"global", "", l.global.TPM},
		{"model", modelAlias, l.modelTPM(modelAlias)},
		{"key", keyID, l.keyTPM(keyID)},
	}

	for _, layer := range tpmLayers {
		if layer.limit <= 0 {
			continue
		}

		currKey := tpmKey(layer.scope, layer.id, ts)
		prevKey := tpmKey(layer.scope, layer.id, prevTs)

		currCount := l.getCount(ctx, currKey)
		prevCount := l.getCount(ctx, prevKey)

		estimated := float64(currCount) + float64(prevCount)*(1.0-weight)
		if estimated >= float64(layer.limit) {
			l.undo(ctx, incremented)
			status.BlockReason = "limit_exceeded"
			status.TPMCurrent = int(estimated)
			status.TPMLimit = layer.limit
			return false, status, nil
		}
	}

	return true, status, nil
}

func (l *Limiter) getCount(ctx context.Context, key string) int64 {
	val, ok := l.cache.Get(ctx, key)
	if !ok {
		return 0
	}
	count, _ := strconv.ParseInt(string(val), 10, 64)
	return count
}

func (l *Limiter) undo(ctx context.Context, keys []string) {
	for _, k := range keys {
		_, _ = l.cache.IncrBy(ctx, k, -1, 61*time.Second)
	}
}

func (l *Limiter) RecordTokens(ctx context.Context, keyID, modelAlias string, actualTokens int) error {
	if actualTokens <= 0 {
		return nil
	}
	ts := windowTimestamp()
	delta := int64(actualTokens)
	ttl := 61 * time.Second

	keys := []string{
		tpmKey("global", "", ts),
		tpmKey("model", modelAlias, ts),
		tpmKey("key", keyID, ts),
	}
	for _, k := range keys {
		if _, err := l.cache.IncrBy(ctx, k, delta, ttl); err != nil {
			return fmt.Errorf("ratelimit: record tokens: %w", err)
		}
	}
	return nil
}

func (l *Limiter) Check(ctx context.Context) error {
	// Verify both read and write capability with a round-trip test.
	if err := l.cache.Set(ctx, "health:probe", []byte("1"), 10*time.Second); err != nil {
		return fmt.Errorf("cache write: %w", err)
	}
	if _, ok := l.cache.Get(ctx, "health:probe"); !ok {
		return fmt.Errorf("cache read: wrote but could not read back")
	}
	return nil
}

func (l *Limiter) logActiveLimit(keyID, model string, gRPM, gTPM, mRPM, mTPM, kRPM, kTPM int) {
	key := model + ":" + keyID
	if _, loaded := l.loggedLimits.LoadOrStore(key, true); !loaded {
		slog.Warn("⚠ LIMIT ACTIVE",
			"key", keyID, "model", model,
			"global", fmt.Sprintf("rpm=%d tpm=%d", gRPM, gTPM),
			"model", fmt.Sprintf("rpm=%d tpm=%d", mRPM, mTPM),
			"key_limit", fmt.Sprintf("rpm=%d tpm=%d", kRPM, kTPM),
		)
	}
}

func windowTimestamp() int64 {
	return time.Now().Unix() / 60
}

func rpmKey(scope, id string, ts int64) string {
	if id == "" {
		return fmt.Sprintf("rl:rpm:%s:%d", scope, ts)
	}
	return fmt.Sprintf("rl:rpm:%s:%s:%d", scope, id, ts)
}

func tpmKey(scope, id string, ts int64) string {
	if id == "" {
		return fmt.Sprintf("rl:tpm:%s:%d", scope, ts)
	}
	return fmt.Sprintf("rl:tpm:%s:%s:%d", scope, id, ts)
}

func (l *Limiter) modelRPM(alias string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if cfg, ok := l.modelLimits[alias]; ok {
		return cfg.RPM
	}
	return 0
}

func (l *Limiter) modelTPM(alias string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if cfg, ok := l.modelLimits[alias]; ok {
		return cfg.TPM
	}
	return 0
}

func (l *Limiter) keyRPM(keyID string) int {
	if l.keyLimits == nil {
		return 0
	}
	cfg := l.keyLimits(keyID)
	if cfg == nil {
		return 0
	}
	return cfg.RPM
}

func (l *Limiter) keyTPM(keyID string) int {
	if l.keyLimits == nil {
		return 0
	}
	cfg := l.keyLimits(keyID)
	if cfg == nil {
		return 0
	}
	return cfg.TPM
}
