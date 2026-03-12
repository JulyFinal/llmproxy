package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/queue"
	"proxyllm/internal/storage"
)

type ChainLogger struct {
	logger storage.Logger
}

func NewChainLogger(logger storage.Logger) *ChainLogger {
	return &ChainLogger{logger: logger}
}

// LogRequestReceived Request arrival
func (l *ChainLogger) LogRequestReceived(ctx context.Context, req *queue.PendingRequest) {
	slog.Info("request_received",
		"request_id", req.ID,
		"session_id", req.SessionID,
		"api_key_id", req.APIKeyID,
		"model", req.ModelAlias,
		"priority", req.Priority,
		"client_ip", extractClientIP(req.Headers),
		"endpoint", req.Path,
	)
}

// LogEnqueued Queued
func (l *ChainLogger) LogEnqueued(ctx context.Context, req *queue.PendingRequest, position, length int) {
	estimatedWait := position * 10 // rough estimate: 10s per request
	
	slog.Info("request_enqueued",
		"request_id", req.ID,
		"queue_position", position,
		"queue_length", length,
		"estimated_wait_sec", estimatedWait,
	)
}

// LogDequeued Dequeued by worker
func (l *ChainLogger) LogDequeued(ctx context.Context, req *queue.PendingRequest, workerID string) {
	queueWaitMs := time.Since(req.Timestamp).Milliseconds()
	
	slog.Info("request_dequeued",
		"request_id", req.ID,
		"worker_id", workerID,
		"queue_wait_ms", queueWaitMs,
	)
}

// LogRateLimitCheck Ratelimit results
func (l *ChainLogger) LogRateLimitCheck(ctx context.Context, req *queue.PendingRequest, allowed bool, status domain.RateLimitStatus) {
	if allowed {
		slog.Info("ratelimit_check",
			"request_id", req.ID,
			"api_key_id", req.APIKeyID,
			"model", req.ModelAlias,
			"rpm_current", status.RPMCurrent,
			"rpm_limit", status.RPMLimit,
			"tpm_current", status.TPMCurrent,
			"tpm_limit", status.TPMLimit,
			"result", "allowed",
		)
	} else {
		slog.Warn("ratelimit_blocked",
			"request_id", req.ID,
			"reason", status.BlockReason,
			"rpm_current", status.RPMCurrent,
			"rpm_limit", status.RPMLimit,
			"tpm_current", status.TPMCurrent,
			"tpm_limit", status.TPMLimit,
			"requeue", true,
		)
	}
}

// LogNodeAttemptStart Node attempt starts
func (l *ChainLogger) LogNodeAttemptStart(ctx context.Context, req *queue.PendingRequest, attempt int, node *domain.ModelNode) {
	slog.Info("node_attempt_start",
		"request_id", req.ID,
		"attempt", attempt,
		"node_id", node.ID,
		"node_name", node.Name,
		"node_base_url", node.BaseURL,
		"timeout_sec", node.TimeoutSec,
	)
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
	errorDetail := ""
	if err != nil {
		errorDetail = err.Error()
	}
	
	slog.Error("node_attempt_failed",
		"request_id", req.ID,
		"attempt", attempt,
		"node_id", node.ID,
		"node_name", node.Name,
		"duration_ms", durationMs,
		"error_type", errorType,
		"error_detail", errorDetail,
		"status_code", statusCode,
		"retryable", IsRetryable(errorType),
		"will_retry", willRetry,
		"next_node", nextNode,
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
	slog.Info("node_attempt_success",
		"request_id", req.ID,
		"attempt", attempt,
		"node_id", node.ID,
		"node_name", node.Name,
		"duration_ms", result.ExecutionMs,
		"status_code", result.StatusCode,
		"prompt_tokens", result.PromptTokens,
		"completion_tokens", result.CompletionTokens,
		"total_tokens", result.TotalTokens,
	)
}

// LogRequestCompleted Request finishes entirely
func (l *ChainLogger) LogRequestCompleted(
	ctx context.Context,
	req *queue.PendingRequest,
	result *domain.ExecutionResult,
) {
	totalDuration := time.Since(req.Timestamp).Milliseconds()
	
	slog.Info("request_completed",
		"request_id", req.ID,
		"status", "success",
		"status_code", result.StatusCode,
		"total_duration_ms", totalDuration,
		"queue_wait_ms", req.QueueWaitMs,
		"execution_ms", result.ExecutionMs,
		"attempts", len(result.Attempts),
		"final_node_id", result.FinalNodeID,
		"prompt_tokens", result.PromptTokens,
		"completion_tokens", result.CompletionTokens,
		"total_tokens", result.TotalTokens,
	)
}

// LogRequestFailed Request fails entirely after exhausting attempts
func (l *ChainLogger) LogRequestFailed(
	ctx context.Context,
	req *queue.PendingRequest,
	attempts []*domain.AttemptLog,
) {
	failedNodes := make([]map[string]any, len(attempts))
	for i, att := range attempts {
		failedNodes[i] = map[string]any{
			"node_id":     att.NodeID,
			"error_type":  att.ErrorType,
			"status_code": att.StatusCode,
			"duration_ms": att.DurationMs,
		}
	}
	
	totalDuration := time.Since(req.Timestamp).Milliseconds()
	
	slog.Error("request_failed",
		"request_id", req.ID,
		"status", "all_nodes_failed",
		"status_code", 502,
		"total_duration_ms", totalDuration,
		"queue_wait_ms", req.QueueWaitMs,
		"execution_ms", req.ExecutionMs,
		"attempts", len(attempts),
		"failed_nodes", failedNodes,
		"final_error", fmt.Sprintf("all %d nodes exhausted", len(attempts)),
	)
}

// LogRequestTimeout Request times out while waiting in queue
func (l *ChainLogger) LogRequestTimeout(
	ctx context.Context,
	req *queue.PendingRequest,
	maxWaitSec int,
	queuePosition int,
) {
	actualWaitMs := time.Since(req.Timestamp).Milliseconds()
	
	slog.Error("request_timeout",
		"request_id", req.ID,
		"status", "queue_timeout",
		"status_code", 504,
		"timeout_type", "max_wait_time",
		"max_wait_time_sec", maxWaitSec,
		"actual_wait_ms", actualWaitMs,
		"queue_position_at_timeout", queuePosition,
		"never_executed", true,
		"reason", fmt.Sprintf("exceeded max_wait_time (%dm), request was still in queue", maxWaitSec/60),
	)
}

// LogRequestCancelled Client disconnected
func (l *ChainLogger) LogRequestCancelled(
	ctx context.Context,
	req *queue.PendingRequest,
	currentAttempt int,
	currentNodeID string,
) {
	totalDuration := time.Since(req.Timestamp).Milliseconds()
	
	slog.Warn("request_cancelled",
		"request_id", req.ID,
		"status", "client_disconnected",
		"status_code", 499,
		"duration_ms", totalDuration,
		"queue_wait_ms", req.QueueWaitMs,
		"execution_ms", req.ExecutionMs,
		"current_attempt", currentAttempt,
		"current_node_id", currentNodeID,
		"reason", "client closed connection",
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

func extractClientIP(headers http.Header) string {
	if ip := headers.Get("X-Forwarded-For"); ip != "" {
		return strings.Split(ip, ",")[0]
	}
	if ip := headers.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return "unknown"
}
