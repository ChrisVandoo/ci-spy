package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func newLogger(out io.Writer, level string) (*slog.Logger, error) {
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
		return nil, fmt.Errorf("invalid --log-level %q: choose debug, info, warn, or error", level)
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: minimum})), nil
}
