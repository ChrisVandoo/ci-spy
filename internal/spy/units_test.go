package spy

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestJobKey(t *testing.T) {
	cases := map[[2]string]string{
		{"Backend", "be-tests-cross-version-migrations (1/2)"}: "Backend / be-tests-cross-version-migrations",
		{"E2E", "e2e-tests-dashboard (3)"}:                     "E2E / e2e-tests-dashboard",
		{"E2E", "e2e shard 4 of 20"}:                           "E2E / e2e",
		{"Backend", "be-tests-mariadb (java 21)"}:              "Backend / be-tests-mariadb (java 21)",
	}
	for in, want := range cases {
		if got := JobKey(in[0], in[1]); got != want {
			t.Errorf("JobKey(%q) = %q, want %q", in[1], got, want)
		}
	}
}

func TestLogExcerpt(t *testing.T) {
	var raw []string
	raw = append(raw, "2026-10-13T10:00:00.0000000Z setup")
	for i := 0; i < 200; i++ {
		raw = append(raw, "2026-10-13T10:00:00.0000000Z noise")
	}
	raw = append(raw,
		"2026-10-13T10:00:01.0000000Z \x1b[31mFAIL in (my-test) (foo_test.clj:3)\x1b[0m",
		"expected: 1", "  actual: 2",
		"ERROR in metabase.query-processor-test/joins-test (joins_test.clj:10)",
		"  ● Dashboard › tabs › renders")
	for i := 0; i < 50; i++ {
		raw = append(raw, "teardown")
	}
	raw = append(raw, "last line")
	lines := CleanLog(strings.Join(raw, "\n"))
	if got := FailingTests(lines, 25); !slices.Equal(got, []string{"my-test", "metabase.query-processor-test/joins-test", "Dashboard › tabs › renders"}) {
		t.Errorf("FailingTests = %v", got)
	}
	ex := Excerpt(lines, 40, 3, 15)
	for _, want := range []string{"FAIL in (my-test)", "actual: 2", "last line"} {
		if !strings.Contains(ex, want) {
			t.Errorf("excerpt missing %q", want)
		}
	}
	if strings.Contains(ex, "2026-10-13T") || strings.Contains(ex, "\x1b") || len(strings.Split(ex, "\n")) > 45 {
		t.Errorf("excerpt not cleaned or too long:\n%s", ex)
	}
}

func TestActiveWindow(t *testing.T) {
	s := ScheduleConfig{Timezone: "America/New_York", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "18:00"}
	for at, want := range map[string]bool{
		"2026-10-13T14:00:00Z": true,  // Tue 10:00
		"2026-10-13T23:30:00Z": false, // Tue 19:30
		"2026-10-17T14:00:00Z": false, // Sat
	} {
		ts, _ := time.Parse(time.RFC3339, at)
		if got := s.InActiveWindow(ts); got != want {
			t.Errorf("%s: got %v", at, got)
		}
	}
}

func TestReleaseBranchOrder(t *testing.T) {
	b := []string{"release-x.9.x", "release-x.58.x", "release-x.57.x"}
	sort.Slice(b, func(i, j int) bool { return compareVersionDesc(b[i], b[j]) })
	if !slices.Equal(b, []string{"release-x.58.x", "release-x.57.x", "release-x.9.x"}) {
		t.Error(b)
	}
}

func TestSince(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-13T12:00:00Z")
	for from, want := range map[string]string{"2026-10-13T11:15:00Z": "45m", "2026-10-13T08:50:00Z": "3h 10m", "2026-10-11T10:00:00Z": "2d 2h"} {
		if got := Since(from, now); got != want {
			t.Errorf("Since(%s) = %s, want %s", from, got, want)
		}
	}
}

func TestParseCommitRef(t *testing.T) {
	for in, want := range map[string]string{
		"ff22433a": "ff22433a",
		"https://github.com/metabase/metabase/commit/FF22433A0000000000000000000000000000000a": "ff22433a0000000000000000000000000000000a",
		"https://github.com/metabase/metabase/commit/ff22433a/checks":                          "ff22433a",
	} {
		if got, err := ParseCommitRef(in); err != nil || got != want {
			t.Errorf("ParseCommitRef(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseCommitRef("master"); err == nil {
		t.Error("branch names are not commits")
	}
}

func TestReconcile(t *testing.T) {
	open := []*Incident{{ID: 7, Keys: []string{"A / x"}, FirstBad: "b", FailingSHAs: []string{"b"}}}
	plan := Reconcile(open, []BreakageGroup{
		{FirstBad: "b", Keys: []string{"A / x"}, FailingSHAs: []string{"c", "b"}, Confirmed: true},
		{FirstBad: "d", Keys: []string{"B / y"}, FailingSHAs: []string{"e", "d"}, Confirmed: true},
		{FirstBad: "z", Keys: []string{"C / z"}, FailingSHAs: []string{"z"}},
	})
	if len(plan.Attach) != 1 || plan.Attach[0].Incident.ID != 7 {
		t.Errorf("attach = %+v", plan.Attach)
	}
	if len(plan.Open) != 1 || plan.Open[0].FirstBad != "d" {
		t.Errorf("open = %+v (unconfirmed z must not open)", plan.Open)
	}
	if len(plan.Resolve) != 0 {
		t.Errorf("resolve = %+v", plan.Resolve)
	}
}
