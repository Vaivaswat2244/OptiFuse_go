package logger

import (
	"log/slog"
	"os"
)

// New creates a structured JSON logger.
// In production (LOG_FORMAT=json) outputs JSON for log aggregators.
// In development outputs human-readable text.
func New(service string) *slog.Logger {
	var handler slog.Handler

	if os.Getenv("LOG_FORMAT") == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	}

	return slog.New(handler).With("service", service)
}
