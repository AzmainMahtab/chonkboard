// Package logger builds the one *slog.Logger the app uses. Structured JSON in
// production so a log aggregator can read it; readable text in development so a
// human can.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

func New(env, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if env == "local" || env == "development" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	l := slog.New(h)
	// Anything reaching for slog.Default() — a library, a stray call — lands in
	// the same stream rather than on a bare stderr logger.
	slog.SetDefault(l)
	return l
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
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
