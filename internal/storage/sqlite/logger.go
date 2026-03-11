package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"proxyllm/internal/domain"
)

// SQLiteLogger implements storage.Logger backed by the same SQLite database.
// Writes are performed synchronously; the async buffering lives in the logging
// package on top of this.
type SQLiteLogger struct {
	db *sql.DB
}

func NewSQLiteLogger(db *sql.DB) *SQLiteLogger {
	return &SQLiteLogger{db: db}
}

// ── Write ──────────────────────────────────────────────────────────────────

// InsertRequestLog writes a single RequestLog row.
func (l *SQLiteLogger) InsertRequestLog(ctx context.Context, log *domain.RequestLog) error {
	_, err := l.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO request_logs
			(id,session_id,timestamp,api_key_id,model_alias,node_id,actual_model,
			 prompt_tokens,completion_tokens,total_tokens,duration_ms,status_code,
			 stream,error_msg,has_detail)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		log.ID, log.SessionID, log.Timestamp.UTC().Format(time.RFC3339Nano),
		log.APIKeyID, log.ModelAlias, log.NodeID, log.ActualModel,
		log.PromptTokens, log.CompletionTokens, log.TotalTokens,
		log.DurationMs, log.StatusCode, boolToInt(log.Stream),
		log.ErrorMsg, boolToInt(log.HasDetail),
	)
	return err
}

// BulkInsertRequestLogs writes multiple rows in a single transaction.
func (l *SQLiteLogger) BulkInsertRequestLogs(ctx context.Context, logs []*domain.RequestLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO request_logs
			(id,session_id,timestamp,api_key_id,model_alias,node_id,actual_model,
			 prompt_tokens,completion_tokens,total_tokens,duration_ms,status_code,
			 stream,error_msg,has_detail)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, log := range logs {
		_, err := stmt.ExecContext(ctx,
			log.ID, log.SessionID, log.Timestamp.UTC().Format(time.RFC3339Nano),
			log.APIKeyID, log.ModelAlias, log.NodeID, log.ActualModel,
			log.PromptTokens, log.CompletionTokens, log.TotalTokens,
			log.DurationMs, log.StatusCode, boolToInt(log.Stream),
			log.ErrorMsg, boolToInt(log.HasDetail),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertDetailLog writes a single DetailLog row.
func (l *SQLiteLogger) InsertDetailLog(ctx context.Context, log *domain.DetailLog) error {
	_, err := l.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO detail_logs (trace_id,session_id,timestamp,request_body,response_body)
		VALUES (?,?,?,?,?)`,
		log.TraceID, log.SessionID,
		log.Timestamp.UTC().Format(time.RFC3339Nano),
		log.RequestBody, log.ResponseBody,
	)
	return err
}

// BulkInsertDetailLogs writes multiple detail rows in one transaction.
func (l *SQLiteLogger) BulkInsertDetailLogs(ctx context.Context, logs []*domain.DetailLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO detail_logs (trace_id,session_id,timestamp,request_body,response_body)
		VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, log := range logs {
		_, err := stmt.ExecContext(ctx,
			log.TraceID, log.SessionID,
			log.Timestamp.UTC().Format(time.RFC3339Nano),
			log.RequestBody, log.ResponseBody,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ── Query ─────────────────────────────────────────────────────────────────

func (l *SQLiteLogger) QueryLogs(ctx context.Context, filter domain.LogFilter) ([]*domain.RequestLog, int64, error) {
	where, args := buildLogFilter(filter)

	var total int64
	countQ := "SELECT COUNT(*) FROM request_logs" + where
	if err := l.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	query := "SELECT id,session_id,timestamp,api_key_id,model_alias,node_id,actual_model," +
		"prompt_tokens,completion_tokens,total_tokens,duration_ms,status_code,stream,error_msg,has_detail " +
		"FROM request_logs" + where +
		fmt.Sprintf(" ORDER BY timestamp DESC LIMIT %d OFFSET %d", pageSize, offset)

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*domain.RequestLog
	for rows.Next() {
		log, err := scanRequestLog(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, log)
	}
	return out, total, rows.Err()
}

func (l *SQLiteLogger) GetDetail(ctx context.Context, traceID string) (*domain.DetailLog, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT trace_id,session_id,timestamp,request_body,response_body FROM detail_logs WHERE trace_id=?`,
		traceID,
	)
	var d domain.DetailLog
	var ts string
	err := row.Scan(&d.TraceID, &d.SessionID, &ts, &d.RequestBody, &d.ResponseBody)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return &d, nil
}

// ── Retention / cleanup ───────────────────────────────────────────────────

// PruneRequestLogs deletes rows exceeding maxRows or older than maxDays.
// Pass 0 to skip that constraint.
func (l *SQLiteLogger) PruneRequestLogs(ctx context.Context, maxRows, maxDays int) error {
	if maxDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -maxDays).Format(time.RFC3339Nano)
		if _, err := l.db.ExecContext(ctx,
			`DELETE FROM request_logs WHERE timestamp < ?`, cutoff); err != nil {
			return err
		}
	}
	if maxRows > 0 {
		if _, err := l.db.ExecContext(ctx, `
			DELETE FROM request_logs WHERE id IN (
				SELECT id FROM request_logs ORDER BY timestamp DESC LIMIT -1 OFFSET ?
			)`, maxRows); err != nil {
			return err
		}
	}
	return nil
}

// PruneDetailLogs same semantics as PruneRequestLogs for detail_logs.
func (l *SQLiteLogger) PruneDetailLogs(ctx context.Context, maxRows, maxDays int) error {
	if maxDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -maxDays).Format(time.RFC3339Nano)
		if _, err := l.db.ExecContext(ctx,
			`DELETE FROM detail_logs WHERE timestamp < ?`, cutoff); err != nil {
			return err
		}
	}
	if maxRows > 0 {
		if _, err := l.db.ExecContext(ctx, `
			DELETE FROM detail_logs WHERE trace_id IN (
				SELECT trace_id FROM detail_logs ORDER BY timestamp DESC LIMIT -1 OFFSET ?
			)`, maxRows); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns aggregated metrics for request_logs since the given timestamp.
func (l *SQLiteLogger) Stats(ctx context.Context, since time.Time) (*domain.LogStats, error) {
	var s domain.LogStats
	err := l.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(total_tokens), 0)
		FROM request_logs
		WHERE timestamp >= ? AND status_code < 500`,
		since.UTC().Format(time.RFC3339Nano),
	).Scan(&s.TotalRequests, &s.PromptTokens, &s.CompletionTokens, &s.TotalTokens)
	return &s, err
}

// RequestLogsSizeBytes estimates the on-disk size of the request_logs table
// by summing the byte length of all stored text columns.
func (l *SQLiteLogger) RequestLogsSizeBytes(ctx context.Context) (int64, error) {
	var size int64
	err := l.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			LENGTH(id) + LENGTH(session_id) + LENGTH(timestamp) +
			LENGTH(api_key_id) + LENGTH(model_alias) + LENGTH(node_id) +
			LENGTH(actual_model) + LENGTH(error_msg) + 64
		), 0) FROM request_logs`).Scan(&size)
	return size, err
}

// DetailLogsSizeBytes estimates the on-disk size of the detail_logs table.
// Detail logs dominate by far due to request/response body content.
func (l *SQLiteLogger) DetailLogsSizeBytes(ctx context.Context) (int64, error) {
	var size int64
	err := l.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			LENGTH(trace_id) + LENGTH(session_id) + LENGTH(timestamp) +
			LENGTH(request_body) + LENGTH(response_body)
		), 0) FROM detail_logs`).Scan(&size)
	return size, err
}

// PruneRequestLogsBySize deletes the oldest rows until the estimated table
// size is below maxBytes. Deletes in batches of 500 to avoid long locks.
func (l *SQLiteLogger) PruneRequestLogsBySize(ctx context.Context, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	for {
		size, err := l.RequestLogsSizeBytes(ctx)
		if err != nil {
			return fmt.Errorf("size check: %w", err)
		}
		if size <= maxBytes {
			return nil
		}
		// Delete the oldest 500 rows.
		if _, err := l.db.ExecContext(ctx, `
			DELETE FROM request_logs WHERE id IN (
				SELECT id FROM request_logs ORDER BY timestamp ASC LIMIT 500
			)`); err != nil {
			return fmt.Errorf("prune by size: %w", err)
		}
	}
}

// PruneDetailLogsBySize deletes the oldest rows until the estimated table
// size is below maxBytes. Batches of 100 (rows are larger).
func (l *SQLiteLogger) PruneDetailLogsBySize(ctx context.Context, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	for {
		size, err := l.DetailLogsSizeBytes(ctx)
		if err != nil {
			return fmt.Errorf("size check: %w", err)
		}
		if size <= maxBytes {
			return nil
		}
		if _, err := l.db.ExecContext(ctx, `
			DELETE FROM detail_logs WHERE trace_id IN (
				SELECT trace_id FROM detail_logs ORDER BY timestamp ASC LIMIT 100
			)`); err != nil {
			return fmt.Errorf("prune detail by size: %w", err)
		}
	}
}

// IncrementalVacuum releases freed pages back to the OS.
// Should be called after large deletions.
func (l *SQLiteLogger) IncrementalVacuum(ctx context.Context) error {
	_, err := l.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return err
}

// ── helpers ───────────────────────────────────────────────────────────────

func buildLogFilter(f domain.LogFilter) (string, []any) {
	var conds []string
	var args []any
	if f.APIKeyID != "" {
		conds = append(conds, "api_key_id=?")
		args = append(args, f.APIKeyID)
	}
	if f.ModelAlias != "" {
		conds = append(conds, "model_alias=?")
		args = append(args, f.ModelAlias)
	}
	if f.NodeID != "" {
		conds = append(conds, "node_id=?")
		args = append(args, f.NodeID)
	}
	if f.StartTime != nil {
		conds = append(conds, "timestamp >= ?")
		args = append(args, f.StartTime.UTC().Format(time.RFC3339Nano))
	}
	if f.EndTime != nil {
		conds = append(conds, "timestamp <= ?")
		args = append(args, f.EndTime.UTC().Format(time.RFC3339Nano))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func scanRequestLog(rows *sql.Rows) (*domain.RequestLog, error) {
	var log domain.RequestLog
	var ts string
	var stream, hasDetail int
	err := rows.Scan(
		&log.ID, &log.SessionID, &ts, &log.APIKeyID,
		&log.ModelAlias, &log.NodeID, &log.ActualModel,
		&log.PromptTokens, &log.CompletionTokens, &log.TotalTokens,
		&log.DurationMs, &log.StatusCode, &stream,
		&log.ErrorMsg, &hasDetail,
	)
	if err != nil {
		return nil, err
	}
	log.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	log.Stream = stream != 0
	log.HasDetail = hasDetail != 0
	return &log, nil
}
