package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestLogLevelFiltersMessages(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  []string
		hide  []string
	}{
		{"debug", []string{"DEBUG", "INFO", "WARN", "ERROR"}, nil},
		{"info", []string{"INFO", "WARN", "ERROR"}, []string{"DEBUG"}},
		{"warn", []string{"WARN", "ERROR"}, []string{"DEBUG", "INFO"}},
		{"error", []string{"ERROR"}, []string{"DEBUG", "INFO", "WARN"}},
		{"DEBUG", []string{"DEBUG", "INFO", "WARN", "ERROR"}, nil},
	} {
		t.Run(tc.level, func(t *testing.T) {
			var out bytes.Buffer
			logger, err := newLogger(&out, tc.level)
			if err != nil {
				t.Fatal(err)
			}
			logger.Debug("debug detail")
			logger.Info("progress")
			logger.Warn("warning")
			logger.Error("failure")
			for _, level := range tc.want {
				if !strings.Contains(out.String(), "level="+level) {
					t.Errorf("missing %s: %s", level, out.String())
				}
			}
			for _, level := range tc.hide {
				if strings.Contains(out.String(), "level="+level) {
					t.Errorf("unexpected %s: %s", level, out.String())
				}
			}
		})
	}
}

func TestInvalidLogLevel(t *testing.T) {
	for _, level := range []string{"", "trace", "info+2", "off"} {
		if _, err := newLogger(io.Discard, level); err == nil || !strings.Contains(err.Error(), "invalid --log-level") {
			t.Errorf("level %q: expected a clear error, got %v", level, err)
		}
	}
}
