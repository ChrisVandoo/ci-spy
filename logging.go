package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func parseLogLevel(level string) (slog.Level, error) {
	var minimum slog.Level
	switch strings.ToLower(level) {
	case "debug":
		minimum = slog.LevelDebug
	case "info":
		minimum = slog.LevelInfo
	case "warn":
		minimum = slog.LevelWarn
	case "error":
		minimum = slog.LevelError
	default:
		return 0, fmt.Errorf("invalid --log-level %q: choose debug, info, warn, or error", level)
	}
	return minimum, nil
}

func newLogger(out io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
}
