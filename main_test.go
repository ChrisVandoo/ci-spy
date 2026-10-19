package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	code := run(args)
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return code, read(stdout.Name()), read(stderr.Name())
}

func TestRunLogLevel(t *testing.T) {
	config := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(config, []byte("repo = ["), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"watch", "analyze"} {
		t.Run(command, func(t *testing.T) {
			args := []string{command}
			if command == "analyze" {
				args = append(args, "abcdef1")
			}
			args = append(args, "--config", config)
			code, out, logs := captureRun(t, args...)
			if code != 1 || out != "" || !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "level=ERROR") {
				t.Fatalf("default: code=%d stdout=%q stderr=%q", code, out, logs)
			}
			code, out, logs = captureRun(t, append(args, "--log-level", "error")...)
			if code != 1 || out != "" || strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "level=ERROR") {
				t.Fatalf("error: code=%d stdout=%q stderr=%q", code, out, logs)
			}
			code, out, logs = captureRun(t, append(args, "--log-level", "invalid")...)
			if code != 2 || out != "" || !strings.Contains(logs, "invalid --log-level") || strings.Contains(logs, "loading config") {
				t.Fatalf("invalid: code=%d stdout=%q stderr=%q", code, out, logs)
			}
		})
	}
}
