package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
)

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colorLogHandler writes colored, human-readable log lines to a terminal.
// Format: HH:MM:SS LEVEL msg key=value key=value
type colorLogHandler struct {
	level slog.Level
	w     *os.File
	attrs []slog.Attr
	group string
}

func (h *colorLogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *colorLogHandler) Handle(_ context.Context, r slog.Record) error {
	// Time — dimmed
	ts := r.Time.Format("15:04:05")

	// Level — colored
	var lvl string
	switch {
	case r.Level >= slog.LevelError:
		lvl = "\033[31mERR\033[0m" // red
	case r.Level >= slog.LevelWarn:
		lvl = "\033[33mWRN\033[0m" // yellow
	case r.Level >= slog.LevelInfo:
		lvl = "\033[36mINF\033[0m" // cyan
	default:
		lvl = "\033[90mDBG\033[0m" // gray
	}

	// Message — bright
	msg := r.Message

	// Attrs — dimmed
	var attrs string
	collect := func(a slog.Attr) bool {
		if a.Key != "" {
			attrs += fmt.Sprintf(" \033[90m%s=\033[0m%s", a.Key, a.Value.String())
		}
		return true
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(collect)

	fmt.Fprintf(h.w, "\033[90m%s\033[0m %s %s%s\n", ts, lvl, msg, attrs)
	return nil
}

func (h *colorLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &colorLogHandler{level: h.level, w: h.w, attrs: append(h.attrs, attrs...), group: h.group}
}

func (h *colorLogHandler) WithGroup(name string) slog.Handler {
	return &colorLogHandler{level: h.level, w: h.w, attrs: h.attrs, group: name}
}

// formatLogLine parses a JSON log line (from the log file) and formats it
// with the same color scheme as the live colorLogHandler output.
func formatLogLine(line string) string {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return line
	}

	var ts string
	if raw, ok := entry["time"]; ok {
		var t time.Time
		if err := json.Unmarshal(raw, &t); err == nil {
			ts = t.Format("15:04:05")
		}
	}

	var lvl string
	if raw, ok := entry["level"]; ok {
		var level string
		json.Unmarshal(raw, &level)
		switch strings.ToUpper(level) {
		case "ERROR":
			lvl = "\033[31mERR\033[0m"
		case "WARN":
			lvl = "\033[33mWRN\033[0m"
		case "INFO":
			lvl = "\033[36mINF\033[0m"
		default:
			lvl = "\033[90mDBG\033[0m"
		}
	}

	var msg string
	if raw, ok := entry["msg"]; ok {
		json.Unmarshal(raw, &msg)
	}

	// Collect remaining keys as attrs, sorted for stable output
	var attrs []string
	for k, v := range entry {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		var val any
		json.Unmarshal(v, &val)
		attrs = append(attrs, fmt.Sprintf("\033[90m%s=\033[0m%v", k, val))
	}
	sort.Strings(attrs)

	attrStr := ""
	if len(attrs) > 0 {
		attrStr = " " + strings.Join(attrs, " ")
	}

	return fmt.Sprintf("\033[90m%s\033[0m %s %s%s", ts, lvl, msg, attrStr)
}

// multiHandler fans out log records to multiple handlers.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(_ context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(context.Background(), level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make(multiHandler, len(m))
	for i, h := range m {
		handlers[i] = h.WithAttrs(attrs)
	}
	return handlers
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	handlers := make(multiHandler, len(m))
	for i, h := range m {
		handlers[i] = h.WithGroup(name)
	}
	return handlers
}
