package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/queue"
	"proxyllm/internal/storage"
)

type ChainLogger struct {
	logger          storage.Logger
	rlBlockedCount  atomic.Int64
	rlLastLogTime   atomic.Int64 // unix seconds
}

func NewChainLogger(logger storage.Logger) *ChainLogger {
	return &ChainLogger{logger: logger}
}

// LogRequestReceived Request arrival
func (l *ChainLogger) LogRequestReceived(ctx context.Context, req *queue.PendingRequest) {
	slog.Info("→ REQ",
		"id", short(req.ID),
		"model", req.ModelAlias,
		"key", short(req.APIKeyID),
		"ip", extractClientIP(req.Headers, req.RemoteAddr),
	)
}

// LogEnqueued Queued
func (l *ChainLogger) LogEnqueued(ctx context.Context, req *queue.PendingRequest, position, length int) {
	if length > 1 {
		slog.Info("⇢ QUEUE",
			"id", short(req.ID),
			"pos", position,
			"len", length,
		)
	}
}

// LogDequeued Dequeued by worker
func (l *ChainLogger) LogDequeued(ctx context.Context, req *queue.PendingRequest, workerID string) {
	waitMs := time.Since(req.Timestamp).Milliseconds()
	if waitMs > 500 {
		slog.Info("⇠ DEQUEUE",
			"id", short(req.ID),
			"worker", workerID,
			"wait", fmtMs(waitMs),
		)
	}
}

// LogRateLimitCheck Ratelimit results
func (l *ChainLogger) LogRateLimitCheck(ctx context.Context, req *queue.PendingRequest, allowed bool, status domain.RateLimitStatus) {
	if allowed {
		return
	}
	count := l.rlBlockedCount.Add(1)
	now := time.Now().Unix()
	last := l.rlLastLogTime.Load()
	if now-last < 5 {
		return
	}
	if !l.rlLastLogTime.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("⚠ RATELIMIT",
		"blocked", count,
		"reason", status.BlockReason,
		"rpm", fmt.Sprintf("%d/%d", status.RPMCurrent, status.RPMLimit),
		"tpm", fmt.Sprintf("%d/%d", status.TPMCurrent, status.TPMLimit),
	)
	l.rlBlockedCount.Store(0)
}

// LogNodeAttemptStart Node attempt starts
func (l *ChainLogger) LogNodeAttemptStart(ctx context.Context, req *queue.PendingRequest, attempt int, node *domain.ModelNode) {
	if attempt > 1 {
		slog.Info("↻ RETRY",
			"id", short(req.ID),
			"attempt", attempt,
			"node", node.Name,
		)
	}
}

// LogNodeAttemptFailed Node attempt fails
func (l *ChainLogger) LogNodeAttemptFailed(
	ctx context.Context,
	req *queue.PendingRequest,
	attempt int,
	node *domain.ModelNode,
	err error,
	statusCode int,
	durationMs int64,
	willRetry bool,
	nextNode string,
) {
	errorType := ClassifyError(err, statusCode)
	detail := errorType
	if err != nil {
		detail = err.Error()
	}
	retry := "no"
	if willRetry {
		retry = "yes"
	}
	slog.Error("✗ NODE FAIL",
		"id", short(req.ID),
		"node", node.Name,
		"status", statusCode,
		"error", detail,
		"time", fmtMs(durationMs),
		"retry", retry,
	)
}

// LogNodeAttemptSuccess Node attempt succeeds
func (l *ChainLogger) LogNodeAttemptSuccess(
	ctx context.Context,
	req *queue.PendingRequest,
	attempt int,
	node *domain.ModelNode,
	result *domain.ExecutionResult,
) {
	// Success is logged in LogRequestCompleted, skip here to reduce noise.
}

// LogRequestCompleted Request finishes entirely
func (l *ChainLogger) LogRequestCompleted(
	ctx context.Context,
	req *queue.PendingRequest,
	result *domain.ExecutionResult,
) {
	totalMs := time.Since(req.Timestamp).Milliseconds()
	slog.Info("✓ DONE",
		"id", short(req.ID),
		"model", req.ModelAlias,
		"node", result.FinalNodeID,
		"tokens", fmt.Sprintf("%d/%d/%d", result.PromptTokens, result.CompletionTokens, result.TotalTokens),
		"time", fmtMs(totalMs),
		"attempts", len(result.Attempts),
	)
}

// LogRequestFailed Request fails entirely after exhausting attempts
func (l *ChainLogger) LogRequestFailed(
	ctx context.Context,
	req *queue.PendingRequest,
	attempts []*domain.AttemptLog,
) {
	totalMs := time.Since(req.Timestamp).Milliseconds()
	nodes := make([]string, len(attempts))
	for i, a := range attempts {
		nodes[i] = fmt.Sprintf("%s(%s)", a.NodeID, a.ErrorType)
	}
	slog.Error("✗ FAILED",
		"id", short(req.ID),
		"model", req.ModelAlias,
		"attempts", len(attempts),
		"nodes", strings.Join(nodes, " → "),
		"time", fmtMs(totalMs),
	)
}

// LogRequestTimeout Request times out while waiting in queue
func (l *ChainLogger) LogRequestTimeout(
	ctx context.Context,
	req *queue.PendingRequest,
	maxWaitSec int,
	queuePosition int,
) {
	waitMs := time.Since(req.Timestamp).Milliseconds()
	slog.Error("⏱ TIMEOUT",
		"id", short(req.ID),
		"model", req.ModelAlias,
		"waited", fmtMs(waitMs),
		"limit", fmt.Sprintf("%ds", maxWaitSec),
	)
}

// LogRequestCancelled Client disconnected
func (l *ChainLogger) LogRequestCancelled(
	ctx context.Context,
	req *queue.PendingRequest,
	currentAttempt int,
	currentNodeID string,
) {
	totalMs := time.Since(req.Timestamp).Milliseconds()
	slog.Warn("⊘ CANCELLED",
		"id", short(req.ID),
		"model", req.ModelAlias,
		"time", fmtMs(totalMs),
		"at_attempt", currentAttempt,
	)
}

func ClassifyError(err error, statusCode int) string {
	if err == nil {
		if statusCode >= 500 {
			return "upstream_5xx"
		}
		if statusCode == 429 {
			return "upstream_429"
		}
		if statusCode >= 400 {
			return "upstream_4xx"
		}
		return "unknown"
	}
	
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "client_cancelled"
	}
	
	errStr := err.Error()
	if strings.Contains(errStr, "connection refused") {
		return "connection_refused"
	}
	if strings.Contains(errStr, "connection reset") {
		return "connection_reset"
	}
	if strings.Contains(errStr, "no such host") {
		return "dns_error"
	}
	
	return "unknown_error"
}

func IsRetryable(errorType string) bool {
	retryableErrors := map[string]bool{
		"upstream_timeout":    true,
		"upstream_5xx":        true,
		"upstream_429":        true,
		"connection_refused":  true,
		"connection_reset":    true,
		"dns_error":           true,
	}
	return retryableErrors[errorType]
}

func extractClientIP(headers http.Header, remoteAddr string) string {
	if ip := headers.Get("X-Forwarded-For"); ip != "" {
		return strings.Split(ip, ",")[0]
	}
	if ip := headers.Get("X-Real-IP"); ip != "" {
		return ip
	}
	// Fall back to direct connection IP (strip port)
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func fmtMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
