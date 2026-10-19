package spy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestAnalyzeLogsProgressWithoutChangingReport(t *testing.T) {
	for _, withAI := range []bool{false, true} {
		name := "without AI"
		if withAI {
			name = "with AI"
		}
		t.Run(name, func(t *testing.T) {
			st, _ := LoadState("")
			_, gh, done := newFakeGitHub(repo, newest(history()))
			defer done()
			var analyzer *Analyzer
			if withAI {
				var prompts []string
				var closeAnalyzer func()
				analyzer, closeAnalyzer = fakeAnthropic(cannedAnalysis, &prompts)
				defer closeAnalyzer()
			}
			var logs bytes.Buffer
			d := deps(t, gh, st, analyzer, io.Discard)
			d.Log = slog.New(slog.NewTextHandler(&logs, nil))
			reports, err := AnalyzeCommit(d, bad, "master", at(7))
			if err != nil || len(reports) != 1 {
				t.Fatalf("reports = %v, error = %v", reports, err)
			}
			messages := []string{"collecting CI status", "CI status collected", "gathering evidence", "commit analysis completed"}
			if withAI {
				messages = append(messages, "analyzing evidence", "analysis completed")
			} else {
				messages = append(messages, "analysis unavailable", "ANTHROPIC_API_KEY is not set")
			}
			for _, message := range messages {
				if !strings.Contains(logs.String(), message) {
					t.Errorf("logs missing %q: %s", message, logs.String())
				}
			}
			if strings.Contains(reports[0].Text(), "level=INFO") {
				t.Error("progress logs leaked into report")
			}
			if strings.Contains(logs.String(), "ValidationFailedException") {
				t.Error("logs contain raw evidence")
			}
		})
	}
}

func TestWatchLogsLifecycleAndKeepsReportsSeparate(t *testing.T) {
	st, _ := LoadState("")
	_, gh, done := newFakeGitHub(repo, newest(history()))
	defer done()
	var logs, reports bytes.Buffer
	d := deps(t, gh, st, nil, &reports)
	d.DryRun = true
	d.Log = slog.New(slog.NewTextHandler(&logs, nil))
	if err := Watch(context.Background(), d, true, true); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"watch started", "starting watch pass", "resolved watched branches", "collecting CI status", "opened incident", "reporting incident", "watch pass completed", "watch stopped"} {
		if !strings.Contains(logs.String(), message) {
			t.Errorf("logs missing %q: %s", message, logs.String())
		}
	}
	if !strings.Contains(reports.String(), "master is red") || strings.Contains(reports.String(), "level=INFO") {
		t.Errorf("unexpected report output: %s", reports.String())
	}
}

func TestWatchLogsSkippedCheckAndShutdown(t *testing.T) {
	st, _ := LoadState("")
	var logs bytes.Buffer
	d := deps(t, &GitHub{}, st, nil, io.Discard)
	d.Config.Schedule.Days = nil
	d.Log = slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Watch(ctx, d, false, false); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"outside active window; skipping check", "waiting for next check", "watch stopped"} {
		if !strings.Contains(logs.String(), message) {
			t.Errorf("logs missing %q: %s", message, logs.String())
		}
	}
	if d.GH.Requests != 0 {
		t.Error("made requests outside active window")
	}
}
