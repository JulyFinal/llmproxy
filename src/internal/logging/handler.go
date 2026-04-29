package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// PrettyHandler outputs logs in a clean pipe-delimited format:
//   09:42:30 INFO  | → REQ        | id=5ced7942 model=Qwen3-32B key=dev
//   09:42:36 INFO  | ✓ DONE       | id=5ced7942 model=Qwen3-32B tokens=120/450/570 time=6.6s
//   09:42:36 ERROR | ✗ NODE FAIL  | id=5ced7942 node=qwen3-32b status=502 error=upstream_5xx
type PrettyHandler struct {
	w     io.Writer
	mu    sync.Mutex
	level slog.Level
}

func NewPrettyHandler(w io.Writer, level slog.Level) *PrettyHandler {
	return &PrettyHandler{w: w, level: level}
}

func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format(time.TimeOnly) // HH:MM:SS

	lvl := r.Level.String()
	switch r.Level {
	case slog.LevelInfo:
		lvl = "INFO "
	case slog.LevelWarn:
		lvl = "WARN "
	case slog.LevelError:
		lvl = "ERROR"
	case slog.LevelDebug:
		lvl = "DEBUG"
	}

	// Pad message to 14 display-width chars for alignment.
	msg := r.Message
	msgWidth := 0
	for _, r := range msg {
		if r > 0x2000 {
			msgWidth += 2 // wide symbol
		} else {
			msgWidth++
		}
	}
	if msgWidth < 16 {
		for i := msgWidth; i < 16; i++ {
			msg += " "
		}
	}

	// Collect attrs
	var attrs string
	r.Attrs(func(a slog.Attr) bool {
		if attrs != "" {
			attrs += " "
		}
		attrs += fmt.Sprintf("%s=%v", a.Key, a.Value.Any())
		return true
	})

	line := fmt.Sprintf("%s %s | %s | %s\n", ts, lvl, msg, attrs)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write([]byte(line))
	return err
}

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *PrettyHandler) WithGroup(name string) slog.Handler       { return h }
