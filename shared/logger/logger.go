package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New creates a structured logger.
// LOG_FORMAT=json emits JSON for log aggregators; anything else is human-readable text.
// LOG_LEVEL selects the threshold: debug, info, warn or error. Defaults to info.
func New(service string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: levelFromEnv()}

	var handler slog.Handler
	if os.Getenv("LOG_FORMAT") == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler).With("service", service)
}

// levelFromEnv reads LOG_LEVEL, defaulting to info.
//
// This used to be hardcoded to Debug. The optimizer logs one line per function
// per request at that level, so on a busy cluster the debug output alone would
// dominate log volume — and log ingestion is billed by volume. Debug stays
// available, but has to be asked for.
func levelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
