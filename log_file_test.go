package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLogFileAppendsAndRespectsLevel(t *testing.T) {
	for _, command := range []string{"watch", "analyze"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "invalid.toml")
			if err := os.WriteFile(config, []byte("repo = ["), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "ci-spy.log")
			args := []string{command, "--config", config, "--log-file", path, "--log-level", "error"}
			if command == "analyze" {
				args = append(args, "abcdef1")
			}
			for i := 0; i < 2; i++ {
				code, out, stderr := captureRun(t, args...)
				if code != 1 || out != "" || stderr != "" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
				}
			}
			logs, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(logs), "loading config failed") != 2 || strings.Contains(string(logs), "level=INFO") {
				t.Errorf("expected two appended errors and no info logs: %s", logs)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0077 != 0 {
				t.Errorf("log file is readable by others: %v", info.Mode())
			}
		})
	}
}

func TestRunLogFileFailure(t *testing.T) {
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing", "ci-spy.log")} {
		code, out, stderr := captureRun(t, "watch", "--once", "--log-file", path)
		if code != 1 || out != "" || !strings.Contains(stderr, "open log file:") {
			t.Errorf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	}
}

func TestInvalidLevelDoesNotCreateLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci-spy.log")
	code, _, stderr := captureRun(t, "watch", "--log-file", path, "--log-level", "invalid")
	if code != 2 || !strings.Contains(stderr, "invalid --log-level") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("invalid arguments created a log file: %v", err)
	}
}

func TestWatchLogFileLifecycle(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	dir := t.TempDir()
	config := filepath.Join(dir, "ci-spy.toml")
	content := fmt.Sprintf("dataDir = %q\n[schedule]\ndays = []\n", filepath.Join(dir, "data"))
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ci-spy.log")
	code, out, stderr := captureRun(t, "watch", "--once", "--no-ai", "--config", config, "--log-file", path, "--log-level", "debug")
	if code != 0 || out != "" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	logs, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"config loaded", "watch started", "outside active window", "watch stopped", "command completed"} {
		if !strings.Contains(string(logs), message) {
			t.Errorf("log file missing %q: %s", message, logs)
		}
	}
	if strings.Contains(string(logs), "test-token") {
		t.Error("log file contains token")
	}
}
