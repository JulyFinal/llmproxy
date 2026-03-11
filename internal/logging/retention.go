package logging

import (
	"context"
	"log/slog"
	"time"

	"proxyllm/internal/storage/sqlite"
)

const mb = int64(1024 * 1024)

// RetentionCleaner enforces log retention policies on a schedule.
// Policies are applied in order: rows → days → size.
// All three can be combined; set any to 0 to disable that constraint.
type RetentionCleaner struct {
	sl *sqlite.SQLiteLogger

	// Tier-1 (request_logs) limits
	basicMaxRows   int
	basicMaxDays   int
	basicMaxSizeMB int // 0 = disabled; default 8192 (8 GB)

	// Tier-2 (detail_logs) limits
	detailMaxRows   int
	detailMaxDays   int
	detailMaxSizeMB int // 0 = disabled; default 2048 (2 GB)

	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewRetentionCleaner creates and immediately starts the background cleaner.
func NewRetentionCleaner(
	sl *sqlite.SQLiteLogger,
	basicMaxRows, basicMaxDays, basicMaxSizeMB,
	detailMaxRows, detailMaxDays, detailMaxSizeMB int,
	interval time.Duration,
) *RetentionCleaner {
	r := &RetentionCleaner{
		sl:              sl,
		basicMaxRows:    basicMaxRows,
		basicMaxDays:    basicMaxDays,
		basicMaxSizeMB:  basicMaxSizeMB,
		detailMaxRows:   detailMaxRows,
		detailMaxDays:   detailMaxDays,
		detailMaxSizeMB: detailMaxSizeMB,
		interval:        interval,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
	go r.run()
	return r
}

func (r *RetentionCleaner) Stop() {
	close(r.stopCh)
	<-r.doneCh
}

func (r *RetentionCleaner) run() {
	defer close(r.doneCh)
	r.clean() // run once immediately on startup
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.clean()
		case <-r.stopCh:
			return
		}
	}
}

func (r *RetentionCleaner) clean() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pruned := false

	// ── Tier-1: request_logs ─────────────────────────────────────────────────
	if err := r.sl.PruneRequestLogs(ctx, r.basicMaxRows, r.basicMaxDays); err != nil {
		slog.Error("retention: prune request logs (rows/days)", "err", err)
	} else if r.basicMaxRows > 0 || r.basicMaxDays > 0 {
		pruned = true
	}

	if r.basicMaxSizeMB > 0 {
		maxBytes := int64(r.basicMaxSizeMB) * mb
		before, _ := r.sl.RequestLogsSizeBytes(ctx)
		if before > maxBytes {
			slog.Info("retention: request_logs over size limit",
				"size_mb", before/mb, "limit_mb", r.basicMaxSizeMB)
			if err := r.sl.PruneRequestLogsBySize(ctx, maxBytes); err != nil {
				slog.Error("retention: prune request logs (size)", "err", err)
			} else {
				after, _ := r.sl.RequestLogsSizeBytes(ctx)
				slog.Info("retention: request_logs pruned",
					"before_mb", before/mb, "after_mb", after/mb)
				pruned = true
			}
		}
	}

	// ── Tier-2: detail_logs ───────────────────────────────────────────────────
	if err := r.sl.PruneDetailLogs(ctx, r.detailMaxRows, r.detailMaxDays); err != nil {
		slog.Error("retention: prune detail logs (rows/days)", "err", err)
	} else if r.detailMaxRows > 0 || r.detailMaxDays > 0 {
		pruned = true
	}

	if r.detailMaxSizeMB > 0 {
		maxBytes := int64(r.detailMaxSizeMB) * mb
		before, _ := r.sl.DetailLogsSizeBytes(ctx)
		if before > maxBytes {
			slog.Info("retention: detail_logs over size limit",
				"size_mb", before/mb, "limit_mb", r.detailMaxSizeMB)
			if err := r.sl.PruneDetailLogsBySize(ctx, maxBytes); err != nil {
				slog.Error("retention: prune detail logs (size)", "err", err)
			} else {
				after, _ := r.sl.DetailLogsSizeBytes(ctx)
				slog.Info("retention: detail_logs pruned",
					"before_mb", before/mb, "after_mb", after/mb)
				pruned = true
			}
		}
	}

	// Only vacuum if we actually deleted anything — it's an expensive operation.
	if pruned {
		if err := r.sl.IncrementalVacuum(ctx); err != nil {
			slog.Warn("retention: incremental vacuum", "err", err)
		}
	}

	slog.Info("retention: cycle complete",
		"basic_size_mb", func() int64 { s, _ := r.sl.RequestLogsSizeBytes(ctx); return s / mb }(),
		"detail_size_mb", func() int64 { s, _ := r.sl.DetailLogsSizeBytes(ctx); return s / mb }(),
	)
}
