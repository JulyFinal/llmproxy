# ProxyLLM 完整链路日志设计

**版本**: v1.0  
**日期**: 2026-03-11  
**目标**: 记录每个环节，明确错误归因，支持问题排查

---

## 日志设计原则

1. **完整性** - 记录请求的每个生命周期阶段
2. **可追溯** - 通过 request_id 串联整个链路
3. **明确归因** - 清楚标识谁超时、谁报错、什么原因
4. **结构化** - JSON 格式，便于查询和分析
5. **性能友好** - 异步写入，不阻塞主流程

---

## 日志事件类型

### 1. 请求生命周期事件

| 事件 | 说明 | 级别 |
|------|------|------|
| `request_received` | 请求到达 | INFO |
| `request_enqueued` | 入队成功 | INFO |
| `request_dequeued` | 被 worker 取走 | INFO |
| `request_completed` | 请求成功 | INFO |
| `request_failed` | 请求失败 | ERROR |
| `request_timeout` | 队列等待超时 | ERROR |
| `request_cancelled` | 客户端断开 | WARN |

### 2. 限流事件

| 事件 | 说明 | 级别 |
|------|------|------|
| `ratelimit_check` | 限流检查通过 | INFO |
| `ratelimit_blocked` | 限流阻塞 | WARN |

### 3. 节点尝试事件

| 事件 | 说明 | 级别 |
|------|------|------|
| `node_attempt_start` | 开始尝试节点 | INFO |
| `node_attempt_success` | 节点成功 | INFO |
| `node_attempt_failed` | 节点失败 | ERROR |

---

## 日志字段规范

### 通用字段

所有日志都包含：

```json
{
  "event": "事件类型",
  "request_id": "req-abc123",
  "session_id": "sess-xyz",
  "timestamp": "2026-03-11T17:00:00.000Z"
}
```

### 请求到达 (request_received)

```json
{
  "event": "request_received",
  "request_id": "req-abc123",
  "session_id": "sess-xyz",
  "timestamp": "2026-03-11T17:00:00.000Z",
  "api_key_id": "key-001",
  "model": "gpt-4",
  "priority": 5,
  "client_ip": "192.168.1.100",
  "endpoint": "/v1/chat/completions"
}
```

### 入队 (request_enqueued)

```json
{
  "event": "request_enqueued",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:00:00.010Z",
  "queue_position": 12,
  "queue_length": 15,
  "estimated_wait_sec": 45
}
```

### 出队 (request_dequeued)

```json
{
  "event": "request_dequeued",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:00:45.000Z",
  "worker_id": "worker-3",
  "queue_wait_ms": 45000
}
```

### 限流检查 (ratelimit_check)

```json
{
  "event": "ratelimit_check",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:00:45.010Z",
  "api_key_id": "key-001",
  "model": "gpt-4",
  "rpm_current": 45,
  "rpm_limit": 50,
  "tpm_current": 8500,
  "tpm_limit": 10000,
  "result": "allowed"
}
```

### 限流阻塞 (ratelimit_blocked)

```json
{
  "event": "ratelimit_blocked",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:00:45.010Z",
  "reason": "tpm_exceeded",
  "tpm_current": 10200,
  "tpm_limit": 10000,
  "requeue": true
}
```

### 节点尝试开始 (node_attempt_start)

```json
{
  "event": "node_attempt_start",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:00:45.020Z",
  "attempt": 1,
  "node_id": "node-openai-primary",
  "node_name": "OpenAI Primary",
  "node_base_url": "https://api.openai.com",
  "timeout_sec": 120
}
```

### 节点尝试失败 (node_attempt_failed)

```json
{
  "event": "node_attempt_failed",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:02:45.020Z",
  "attempt": 1,
  "node_id": "node-openai-primary",
  "node_name": "OpenAI Primary",
  "duration_ms": 120000,
  "error_type": "upstream_timeout",
  "error_detail": "context deadline exceeded",
  "status_code": 0,
  "retryable": true,
  "will_retry": true,
  "next_node": "node-openai-backup"
}
```

**错误类型枚举**:
- `upstream_timeout` - 上游超时
- `upstream_5xx` - 上游 5xx 错误
- `upstream_429` - 上游限流
- `upstream_4xx` - 上游 4xx 错误
- `connection_refused` - 连接被拒绝
- `connection_reset` - 连接重置
- `dns_error` - DNS 解析失败
- `client_cancelled` - 客户端取消
- `unknown_error` - 未知错误

### 节点尝试成功 (node_attempt_success)

```json
{
  "event": "node_attempt_success",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:02:50.000Z",
  "attempt": 2,
  "node_id": "node-openai-backup",
  "node_name": "OpenAI Backup",
  "duration_ms": 4980,
  "status_code": 200,
  "prompt_tokens": 50,
  "completion_tokens": 120,
  "total_tokens": 170
}
```

### 请求成功 (request_completed)

```json
{
  "event": "request_completed",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:02:50.100Z",
  "status": "success",
  "status_code": 200,
  "total_duration_ms": 170100,
  "queue_wait_ms": 45000,
  "execution_ms": 125100,
  "attempts": 2,
  "final_node_id": "node-openai-backup",
  "prompt_tokens": 50,
  "completion_tokens": 120,
  "total_tokens": 170
}
```

### 请求失败 - 所有节点失败 (request_failed)

```json
{
  "event": "request_failed",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:08:00.000Z",
  "status": "all_nodes_failed",
  "status_code": 502,
  "total_duration_ms": 480000,
  "queue_wait_ms": 45000,
  "execution_ms": 435000,
  "attempts": 3,
  "failed_nodes": [
    {
      "node_id": "node-openai-primary",
      "error_type": "upstream_timeout",
      "duration_ms": 120000
    },
    {
      "node_id": "node-openai-backup",
      "error_type": "upstream_5xx",
      "status_code": 503,
      "duration_ms": 5000
    },
    {
      "node_id": "node-claude-fallback",
      "error_type": "connection_refused",
      "duration_ms": 100
    }
  ],
  "final_error": "all 3 nodes exhausted"
}
```

### 请求超时 - 队列等待超时 (request_timeout)

```json
{
  "event": "request_timeout",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:30:00.000Z",
  "status": "queue_timeout",
  "status_code": 504,
  "timeout_type": "max_wait_time",
  "max_wait_time_sec": 1800,
  "actual_wait_ms": 1800000,
  "queue_position_at_timeout": 8,
  "never_executed": true,
  "reason": "exceeded max_wait_time (30m), request was still in queue"
}
```

### 客户端断开 (request_cancelled)

```json
{
  "event": "request_cancelled",
  "request_id": "req-abc123",
  "timestamp": "2026-03-11T17:05:00.000Z",
  "status": "client_disconnected",
  "status_code": 499,
  "duration_ms": 300000,
  "queue_wait_ms": 45000,
  "execution_ms": 255000,
  "current_attempt": 2,
  "current_node_id": "node-openai-backup",
  "reason": "client closed connection"
}
```

---

## 日志查询示例

### 1. 查看某个请求的完整链路

```bash
grep "req-abc123" /var/log/proxyllm.log | jq .
```

### 2. 查看所有超时的请求

```bash
grep '"event":"request_timeout"' /var/log/proxyllm.log | jq .
```

### 3. 查看某个节点的失败率

```bash
grep '"event":"node_attempt_failed"' /var/log/proxyllm.log | \
  jq -r 'select(.node_id=="node-openai-primary") | .error_type' | \
  sort | uniq -c | sort -rn
```

输出示例:
```
  45 upstream_timeout
  12 upstream_5xx
   3 connection_refused
```

### 4. 查看队列等待时间分布

```bash
grep '"event":"request_completed"' /var/log/proxyllm.log | \
  jq -r '.queue_wait_ms' | \
  awk '{
    sum+=$1; count++; 
    if($1>max) max=$1; 
    if(min=="" || $1<min) min=$1
  } 
  END {
    print "avg:", sum/count, "ms";
    print "min:", min, "ms";
    print "max:", max, "ms"
  }'
```

### 5. 统计各错误类型占比

```bash
grep '"event":"node_attempt_failed"' /var/log/proxyllm.log | \
  jq -r '.error_type' | \
  sort | uniq -c | sort -rn | \
  awk '{printf "%s: %.2f%%\n", $2, $1/total*100}'
```

### 6. 查看重试成功率

```bash
# 统计最终成功但中间有失败的请求
grep '"event":"request_completed"' /var/log/proxyllm.log | \
  jq -r 'select(.attempts > 1) | .attempts' | \
  wc -l
```

---

## 日志存储建议

### 1. 文件日志

```toml
[logging]
log_file = "/var/log/proxyllm/app.log"
log_level = "info"
log_format = "json"
max_size_mb = 100
max_backups = 10
max_age_days = 30
compress = true
```

### 2. 日志轮转

使用 `lumberjack` 库自动轮转：

```go
import "gopkg.in/natefinch/lumberjack.v2"

logger := &lumberjack.Logger{
    Filename:   "/var/log/proxyllm/app.log",
    MaxSize:    100, // MB
    MaxBackups: 10,
    MaxAge:     30, // days
    Compress:   true,
}
```

### 3. 集中式日志 (可选)

- **ELK Stack**: Elasticsearch + Logstash + Kibana
- **Loki**: Grafana Loki + Promtail
- **CloudWatch Logs**: AWS 环境

---

## 日志完整性检查清单

- [x] 请求到达时间
- [x] 入队时间和位置
- [x] 出队时间和等待时长
- [x] 限流检查结果
- [x] 每次节点尝试的开始时间
- [x] 每次节点尝试的结果
- [x] 失败原因分类
- [x] 重试决策
- [x] 最终结果
- [x] 完整耗时分解
- [x] Token 使用量
- [x] 客户端断开检测
- [x] 队列超时检测

---

**相关文档**: `QUEUE_AND_RETRY_DESIGN.md` (架构设计)

## ChainLogger 实现代码

```go
// internal/logging/chain_logger.go
package logging

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "strings"
    "time"

    "proxyllm/internal/domain"
)

type ChainLogger struct {
    logger storage.Logger
}

func NewChainLogger(logger storage.Logger) *ChainLogger {
    return &ChainLogger{logger: logger}
}

// 请求到达
func (l *ChainLogger) LogRequestReceived(ctx context.Context, req *PendingRequest) {
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

// 入队
func (l *ChainLogger) LogEnqueued(ctx context.Context, req *PendingRequest, position, length int) {
    estimatedWait := position * 10 // 粗略估算：每个请求 10 秒
    
    slog.Info("request_enqueued",
        "request_id", req.ID,
        "queue_position", position,
        "queue_length", length,
        "estimated_wait_sec", estimatedWait,
    )
}

// 出队
func (l *ChainLogger) LogDequeued(ctx context.Context, req *PendingRequest, workerID string) {
    queueWaitMs := time.Since(req.Timestamp).Milliseconds()
    
    slog.Info("request_dequeued",
        "request_id", req.ID,
        "worker_id", workerID,
        "queue_wait_ms", queueWaitMs,
    )
}

// 限流检查
func (l *ChainLogger) LogRateLimitCheck(ctx context.Context, req *PendingRequest, allowed bool, status RateLimitStatus) {
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

// 节点尝试开始
func (l *ChainLogger) LogNodeAttemptStart(ctx context.Context, req *PendingRequest, attempt int, node *domain.ModelNode) {
    slog.Info("node_attempt_start",
        "request_id", req.ID,
        "attempt", attempt,
        "node_id", node.ID,
        "node_name", node.Name,
        "node_base_url", node.BaseURL,
        "node_priority", node.Priority,
        "node_weight", node.Weight,
        "timeout_sec", node.TimeoutSec,
    )
}

// 节点尝试失败
func (l *ChainLogger) LogNodeAttemptFailed(
    ctx context.Context,
    req *PendingRequest,
    attempt int,
    node *domain.ModelNode,
    err error,
    statusCode int,
    durationMs int64,
    willRetry bool,
    nextNode string,
) {
    errorType := classifyError(err, statusCode)
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
        "retryable", isRetryable(errorType),
        "will_retry", willRetry,
        "next_node", nextNode,
    )
}

// 节点尝试成功
func (l *ChainLogger) LogNodeAttemptSuccess(
    ctx context.Context,
    req *PendingRequest,
    attempt int,
    node *domain.ModelNode,
    result *ForwardResult,
) {
    slog.Info("node_attempt_success",
        "request_id", req.ID,
        "attempt", attempt,
        "node_id", node.ID,
        "node_name", node.Name,
        "duration_ms", result.DurationMs,
        "status_code", result.StatusCode,
        "prompt_tokens", result.PromptTokens,
        "completion_tokens", result.CompletionTokens,
        "total_tokens", result.TotalTokens,
    )
}

// 请求完成
func (l *ChainLogger) LogRequestCompleted(
    ctx context.Context,
    req *PendingRequest,
    result *ExecutionResult,
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

// 请求失败
func (l *ChainLogger) LogRequestFailed(
    ctx context.Context,
    req *PendingRequest,
    attempts []*AttemptLog,
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

// 请求超时
func (l *ChainLogger) LogRequestTimeout(
    ctx context.Context,
    req *PendingRequest,
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

// 客户端断开
func (l *ChainLogger) LogRequestCancelled(
    ctx context.Context,
    req *PendingRequest,
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

// 错误分类
func classifyError(err error, statusCode int) string {
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

// 判断是否可重试
func isRetryable(errorType string) bool {
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

// 提取客户端 IP
func extractClientIP(headers http.Header) string {
    if ip := headers.Get("X-Forwarded-For"); ip != "" {
        return strings.Split(ip, ",")[0]
    }
    if ip := headers.Get("X-Real-IP"); ip != "" {
        return ip
    }
    return "unknown"
}

// 辅助结构
type RateLimitStatus struct {
    RPMCurrent  int
    RPMLimit    int
    TPMCurrent  int
    TPMLimit    int
    BlockReason string
}

type AttemptLog struct {
    Attempt         int
    NodeID          string
    NodeName        string
    DurationMs      int64
    StatusCode      int
    ErrorType       string
    Error           error
    PromptTokens    int
    CompletionTokens int
}

type ExecutionResult struct {
    StatusCode       int
    Body             []byte
    Headers          http.Header
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    FinalNodeID      string
    Attempts         []*AttemptLog
    ExecutionMs      int64
    Error            string
    ErrorType        string
}
```

---

## 使用示例

### Worker 中集成日志

```go
func (p *WorkerPool) worker(id int) {
    workerID := fmt.Sprintf("worker-%d", id)
    
    for {
        select {
        case <-p.stopCh:
            return
        default:
        }
        
        // 从队列取任务
        req := p.queue.DequeueBlocking()
        if req == nil {
            continue
        }
        
        // 记录出队
        p.chainLogger.LogDequeued(req.Context, req, workerID)
        
        // 检查限流
        allowed, status := p.checkRateLimit(req)
        p.chainLogger.LogRateLimitCheck(req.Context, req, allowed, status)
        
        if !allowed {
            // 重新入队
            time.Sleep(100 * time.Millisecond)
            p.queue.Enqueue(req)
            continue
        }
        
        // 执行转发（含重试）
        result := p.forwardWithRetry(req)
        
        // 发送结果
        select {
        case req.ResultChan <- result:
        case <-req.Context.Done():
            // 客户端已断开
        }
    }
}
```

---

## 监控指标建议

基于日志可以导出以下 Prometheus 指标：

```go
// 请求总数
proxyllm_requests_total{status="success|failed|timeout|cancelled"}

// 请求耗时
proxyllm_request_duration_seconds{quantile="0.5|0.9|0.99"}

// 队列等待时间
proxyllm_queue_wait_seconds{quantile="0.5|0.9|0.99"}

// 队列长度
proxyllm_queue_length{model="gpt-4"}

// 节点尝试次数
proxyllm_node_attempts_total{node_id="xxx", result="success|failed"}

// 节点错误类型
proxyllm_node_errors_total{node_id="xxx", error_type="timeout|5xx|..."}

// 重试成功率
proxyllm_retry_success_rate{model="gpt-4"}
```

---

## 总结

这套日志体系确保：

1. ✅ **完整追踪** - 从请求到达到最终响应的每个环节
2. ✅ **明确归因** - 清楚知道是哪个节点、什么原因导致失败
3. ✅ **性能友好** - 结构化日志，异步写入
4. ✅ **易于查询** - JSON 格式，支持 jq/grep 快速分析
5. ✅ **支持监控** - 可导出 Prometheus 指标

配合 `QUEUE_AND_RETRY_DESIGN.md` 中的架构设计，可以实现高可用、可观测的请求排队和重试系统。
