// Package logging implements the two-tier async logging system.
//
// Tier 1 (RequestLog) and Tier 2 (DetailLog) are written to buffered channels.
// A background worker drains each channel and batch-flushes to SQLite at a
// configurable interval (default 500 ms). This keeps the hot request path
// completely allocation-free after the channel send.
package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/storage"
	"proxyllm/internal/storage/sqlite"
)

// AsyncLogger satisfies storage.Logger.
type AsyncLogger struct {
	sl            *sqlite.SQLiteLogger
	reqCh         chan *domain.RequestLog
	detailCh      chan *domain.DetailLog
	flushInterval time.Duration
	wg            sync.WaitGroup
	once          sync.Once
	stopCh        chan struct{}
}

// New creates an AsyncLogger.
//   - sl: the SQLite log writer
//   - bufferSize: channel capacity (e.g. 4096)
//   - flushInterval: how often to batch-flush to SQLite (e.g. 500ms)
func New(sl *sqlite.SQLiteLogger, bufferSize int, flushInterval time.Duration) *AsyncLogger {
	l := &AsyncLogger{
		sl:            sl,
		reqCh:         make(chan *domain.RequestLog, bufferSize),
		detailCh:      make(chan *domain.DetailLog, bufferSize),
		flushInterval: flushInterval,
		stopCh:        make(chan struct{}),
	}
	l.wg.Add(2)
	go l.runRequestWorker()
	go l.runDetailWorker()
	return l
}

// ── storage.Logger implementation ────────────────────────────────────────────

// AsyncLog enqueues a request log entry. If the channel is full the entry is
// dropped and a warning is emitted (non-blocking, never panics).
func (l *AsyncLogger) AsyncLog(log *domain.RequestLog) {
	select {
	case l.reqCh <- log:
	default:
		slog.Warn("request log channel full, dropping entry", "trace_id", log.ID)
	}
}

// AsyncLogDetail enqueues a detail log entry (non-blocking).
func (l *AsyncLogger) AsyncLogDetail(log *domain.DetailLog) {
	select {
	case l.detailCh <- log:
	default:
		slog.Warn("detail log channel full, dropping entry", "trace_id", log.TraceID)
	}
}

// Stats delegates directly to SQLite (synchronous).
func (l *AsyncLogger) Stats(ctx context.Context, filter domain.LogFilter) (*domain.LogStats, error) {
	return l.sl.Stats(ctx, filter)
}

func (l *AsyncLogger) StatsTimeSeries(ctx context.Context, filter domain.LogFilter, granularity string) ([]*domain.TimeSeriesPoint, error) {
	return l.sl.StatsTimeSeries(ctx, filter, granularity)
}

func (l *AsyncLogger) StatsTop(ctx context.Context, filter domain.LogFilter, groupBy string, limit int) ([]*domain.TopEntity, error) {
	return l.sl.StatsTop(ctx, filter, groupBy, limit)
}

// ExportLogs is synchronous and delegates directly to SQLite.
func (l *AsyncLogger) ExportLogs(ctx context.Context, filter domain.LogFilter) ([]*domain.RequestLog, error) {
	return l.sl.ExportLogs(ctx, filter)
}

// QueryLogs is synchronous and delegates directly to SQLite.
func (l *AsyncLogger) QueryLogs(ctx context.Context, filter domain.LogFilter) ([]*domain.RequestLog, int64, error) {
	return l.sl.QueryLogs(ctx, filter)
}

// GetDetail is synchronous and delegates directly to SQLite.
func (l *AsyncLogger) GetDetail(ctx context.Context, traceID string) (*domain.DetailLog, error) {
	return l.sl.GetDetail(ctx, traceID)
}

// Flush drains both channels synchronously. Called during graceful shutdown.
func (l *AsyncLogger) Flush(ctx context.Context) error {
	// Drain request log channel
	var reqs []*domain.RequestLog
	for {
		select {
		case r := <-l.reqCh:
			reqs = append(reqs, r)
		default:
			goto doneReqs
		}
	}
doneReqs:
	if len(reqs) > 0 {
		if err := l.sl.BulkInsertRequestLogs(ctx, reqs); err != nil {
			slog.Error("flush: bulk insert request logs", "err", err)
		}
	}

	// Drain detail log channel
	var details []*domain.DetailLog
	for {
		select {
		case d := <-l.detailCh:
			details = append(details, d)
		default:
			goto doneDetails
		}
	}
doneDetails:
	if len(details) > 0 {
		if err := l.sl.BulkInsertDetailLogs(ctx, details); err != nil {
			slog.Error("flush: bulk insert detail logs", "err", err)
		}
	}
	return nil
}

// Close stops background workers and waits for them to exit.
func (l *AsyncLogger) Close() error {
	l.once.Do(func() {
		close(l.stopCh)
	})
	l.wg.Wait()
	return nil
}

// ── background workers ────────────────────────────────────────────────────────

func (l *AsyncLogger) runRequestWorker() {
	defer l.wg.Done()
	ticker := time.NewTicker(l.flushInterval)
	defer ticker.Stop()

	var batch []*domain.RequestLog

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := l.sl.BulkInsertRequestLogs(ctx, batch); err != nil {
			slog.Error("request log flush error", "err", err, "count", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case r := <-l.reqCh:
			batch = append(batch, r)
			if len(batch) >= 256 { // flush early if batch is large
				flush()
			}
		case <-ticker.C:
			flush()
		case <-l.stopCh:
			// Drain remaining
			for {
				select {
				case r := <-l.reqCh:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (l *AsyncLogger) runDetailWorker() {
	defer l.wg.Done()
	ticker := time.NewTicker(l.flushInterval)
	defer ticker.Stop()

	var batch []*domain.DetailLog

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := l.sl.BulkInsertDetailLogs(ctx, batch); err != nil {
			slog.Error("detail log flush error", "err", err, "count", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case d := <-l.detailCh:
			batch = append(batch, d)
			if len(batch) >= 64 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-l.stopCh:
			for {
				select {
				case d := <-l.detailCh:
					batch = append(batch, d)
				default:
					flush()
					return
				}
			}
		}
	}
}

// compile-time interface check
var _ storage.Logger = (*AsyncLogger)(nil)
