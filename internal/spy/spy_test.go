package spy

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A breakage shaped like master's cross-version migration failure: first red
// at ff22433a (PR #82773), then a train of red commits, then a fix.

const repo = "metabase/metabase"

var bad = "ff22433a" + strings.Repeat("0", 32)

const migrationLog = `2026-10-13T10:00:00.0000000Z Running cross-version migration tests
2026-10-13T10:00:01.0000000Z Starting Metabase v0.56 against the migrated app DB
2026-10-13T10:00:02.0000000Z FAIL in (migrate-up-and-down-test) (cross_version_test.clj:88)
2026-10-13T10:00:02.0000000Z expected: (= :ok (migrate! :down 56))
2026-10-13T10:00:02.0000000Z   actual: liquibase.exception.ValidationFailedException: changeset v57.2026-10-01T00:00:00 has no rollback
2026-10-13T10:00:03.0000000Z ##[error]Process completed with exit code 1.`

func sha(n int) string { return strings.Repeat("0", 39) + string(rune('0'+n)) }

func at(i int) time.Time {
	return time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC).Add(time.Duration(i) * 30 * time.Minute)
}

func backend(m string) fakeRun {
	c := map[string]string{"pass": "success", "fail": "failure", "pending": ""}[m]
	shard2 := c
	if m == "fail" {
		shard2 = "success"
	}
	return fakeRun{Workflow: "Backend", Conclusion: c, Jobs: []fakeJob{
		{Name: "be-tests-cross-version-migrations (1/2)", Conclusion: c, Log: migrationLog},
		{Name: "be-tests-cross-version-migrations (2/2)", Conclusion: shard2},
		{Name: "be-tests-postgres-latest-ee", Conclusion: "success"},
	}}
}

func e2e(ok bool) fakeRun {
	c := "success"
	if !ok {
		c = "failure"
	}
	return fakeRun{Workflow: "E2E", Conclusion: c, Jobs: []fakeJob{{Name: "e2e-tests-dashboard (3)", Conclusion: c, Log: "Timed out retrying after 4000ms"}}}
}

var frontend = fakeRun{Workflow: "Frontend", Conclusion: "success", Jobs: []fakeJob{{Name: "fe-tests-unit", Conclusion: "success"}}}

func commit(i int, migrations string, e2eOK bool) fakeCommit {
	return fakeCommit{SHA: sha(i), Login: "dev" + string(rune('0'+i)), At: at(i).Format(time.RFC3339), Runs: []fakeRun{backend(migrations), e2e(e2eOK), frontend}}
}

func badCommit() fakeCommit {
	c := commit(3, "fail", true)
	c.SHA, c.Login, c.Name = bad, "migration-author", "Pat Dev"
	c.Message = "Add v57 migration for dashboard tabs (#82773)"
	c.PR = &PR{Number: 82773, Title: "Add v57 migration for dashboard tabs", Author: "migration-author"}
	c.Files = []ChangedFile{
		{Filename: "frontend/src/metabase/dashboard/tabs.tsx", Patch: "+// unrelated ui tweak"},
		{Filename: "resources/migrations/001_update_migrations.yaml", Patch: "+  - changeSet:\n+      id: v57.2026-10-01T00:00:00"},
	}
	return c
}

// oldest first; newest() flips to branch order
func history() []fakeCommit {
	return []fakeCommit{commit(1, "pass", true), commit(2, "pass", true), badCommit(), commit(4, "fail", false), commit(5, "fail", true), commit(6, "pending", true)}
}

func newest(cs []fakeCommit) []fakeCommit {
	out := slices.Clone(cs)
	slices.Reverse(out)
	return out
}

var cannedAnalysis = Analysis{
	Summary:         "v57 migration has no rollback, so the downgrade step of cross-version tests fails",
	Hypothesis:      "PR #82773 adds a changeset without a rollback block.",
	Confidence:      "high",
	Suspect:         AnalysisSuspect{SHA: bad, PRNumber: 82773, Why: "changeSet v57.2026-10-01T00:00:00 lacks rollback"},
	Evidence:        []EvidenceItem{{Claim: "ValidationFailedException names the new changeset", Source: "log: Backend"}},
	ValidationSteps: []string{"git checkout " + bad + " && ./bin/test-cross-version"},
	ProposedFix:     "Add a rollback block to the changeset.",
}

func testConfig(t *testing.T) Config {
	cfg := DefaultConfig()
	cfg.Branches = []string{"master"}
	cfg.Notify = NotifyConfig{ReportsDir: t.TempDir()}
	cfg.People = map[string]Person{"migration-author": {Name: "Pat Dev", SlackID: "U0PAT"}}
	return cfg
}

func deps(t *testing.T, gh *GitHub, st *State, an *Analyzer, out io.Writer) Deps {
	return Deps{GH: gh, State: st, Config: testConfig(t), Analyzer: an, Out: out, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestWatchRedTrainOneIncidentOneAnalysisThenRecovery(t *testing.T) {
	st, _ := LoadState("")
	var prompts []string
	an, closeAn := fakeAnthropic(cannedAnalysis, &prompts)
	defer closeAn()
	cfg := testConfig(t)
	var out bytes.Buffer

	pass := func(commits []fakeCommit, now time.Time) PassResult {
		_, gh, done := newFakeGitHub(repo, newest(commits))
		defer done()
		d := deps(t, gh, st, an, &out)
		d.Config = cfg
		res, err := Pass(d, now)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	first := pass(history(), at(7))
	if len(first.Opened) != 1 || !slices.Equal(first.Notified, first.Opened) {
		t.Fatalf("want one incident opened and notified, got %+v", first)
	}
	inc := st.Incident(first.Opened[0])
	if inc.FirstBad != bad || inc.LastGood != sha(2) {
		t.Errorf("blame window = %s..%s", inc.LastGood, inc.FirstBad)
	}
	if !slices.Equal(inc.Keys, []string{"Backend / be-tests-cross-version-migrations"}) {
		t.Errorf("keys = %v", inc.Keys)
	}
	if !slices.Equal(inc.FailingSHAs, []string{sha(5), sha(4), bad}) {
		t.Errorf("failing = %v", inc.FailingSHAs)
	}
	if len(prompts) != 1 {
		t.Fatalf("want 1 analysis, got %d", len(prompts))
	}
	p := prompts[0]
	for _, want := range []string{"PR #82773", "migrate-up-and-down-test", "ValidationFailedException"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// The migration diff, which matches the failing job, is ranked first.
	if strings.Index(p, "--- diff resources/migrations") > strings.Index(p, "--- diff frontend/") {
		t.Error("migration diff should come before the unrelated frontend diff")
	}
	md, _ := os.ReadFile(filepath.Join(cfg.Notify.ReportsDir, "incident-1.md"))
	for _, want := range []string{"#82773", "Pat Dev", "GitHub @migration-author", "Broken for:** 2h 0m · 3 red commits"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("markdown report missing %q:\n%s", want, md)
		}
	}
	if !strings.Contains(out.String(), "━━ master is red: 1 job failing") {
		t.Errorf("text report not printed:\n%s", out.String())
	}

	// More red commits land: attached silently, no second analysis or ping.
	train := append(history()[:5], commit(6, "fail", true), commit(7, "fail", true))
	second := pass(train, at(8))
	if len(second.Opened) != 0 || !slices.Equal(second.Attached, []int{inc.ID}) || len(second.Notified) != 0 || len(prompts) != 1 {
		t.Errorf("train should attach silently: %+v, analyses %d", second, len(prompts))
	}

	// A fix lands: resolved, and a recovery ping because we pinged about it.
	third := pass(append(train, commit(8, "pass", true)), at(9))
	if !slices.Equal(third.Resolved, []int{inc.ID}) || inc.ResolvedNotifiedAt == nil {
		t.Errorf("want resolved + recovery ping: %+v", third)
	}
}

func TestWatchIgnoresOneOffFlake(t *testing.T) {
	st, _ := LoadState("")
	_, gh, done := newFakeGitHub(repo, newest([]fakeCommit{commit(1, "pass", true), commit(2, "pass", true), commit(3, "pass", false)}))
	defer done()
	res, err := Pass(deps(t, gh, st, nil, io.Discard), at(4))
	if err != nil || len(res.Opened) != 0 {
		t.Fatalf("flake opened an incident: %+v %v", res, err)
	}
}

func TestWatchConfirmsSingleFailureThatFailedRerun(t *testing.T) {
	st, _ := LoadState("")
	rerun := commit(3, "pass", true)
	rerun.Runs = []fakeRun{{Workflow: "E2E", Conclusion: "failure", Attempt: 2, Jobs: []fakeJob{{Name: "e2e-tests-dashboard (3)", Conclusion: "failure"}}}}
	_, gh, done := newFakeGitHub(repo, newest([]fakeCommit{commit(1, "pass", true), commit(2, "pass", true), rerun}))
	defer done()
	res, err := Pass(deps(t, gh, st, nil, io.Discard), at(4))
	if err != nil || len(res.Opened) != 1 {
		t.Fatalf("want an incident: %+v %v", res, err)
	}
	if !strings.Contains(st.Incident(res.Opened[0]).AnalysisError, "ANTHROPIC_API_KEY") {
		t.Error("missing key should be explained in the report")
	}
}

func TestWatchGreenBranchIsCheap(t *testing.T) {
	st, _ := LoadState("")
	f, gh, done := newFakeGitHub(repo, newest([]fakeCommit{commit(1, "pass", true), commit(2, "pass", true)}))
	defer done()
	now := at(2).Add(7 * time.Hour)
	d := deps(t, gh, st, nil, io.Discard)
	Pass(d, now)
	if len(f.calls) != 3 {
		t.Errorf("first pass: want 3 requests, got %d: %v", len(f.calls), f.calls)
	}
	f.calls = nil
	Pass(d, now)
	if len(f.calls) != 1 {
		t.Errorf("settled commits should be cached: %v", f.calls)
	}
}

func TestAnalyzeCommitPrintsSameReport(t *testing.T) {
	st, _ := LoadState("")
	var prompts []string
	an, closeAn := fakeAnthropic(cannedAnalysis, &prompts)
	defer closeAn()
	_, gh, done := newFakeGitHub(repo, newest(history()))
	defer done()
	d := deps(t, gh, st, an, io.Discard)

	// Point at the first red commit by URL: one red commit is enough here.
	reports, err := AnalyzeCommit(d, "https://github.com/metabase/metabase/commit/"+bad, "master", at(7))
	if err != nil || len(reports) != 1 {
		t.Fatalf("got %d reports, %v", len(reports), err)
	}
	text := reports[0].Text()
	for _, want := range []string{"━━ ff22433a on master: 1 job failing", "Suspect change: #82773", "Who:", "Pat Dev", "Hypothesis (high confidence)", "Validate", "Failing jobs", "migrate-up-and-down-test", "Blame window", "last green 00000000"} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
	if len(st.Incidents) != 0 {
		t.Error("analyze must not create watch incidents")
	}

	// Same sections, in the same order, as the watch report for this breakage.
	st2, _ := LoadState("")
	var out bytes.Buffer
	Pass(deps(t, gh, st2, an, &out), at(7))
	if headings(out.String()) != headings(text) {
		t.Errorf("watch and analyze reports differ:\nwatch:   %s\nanalyze: %s", headings(out.String()), headings(text))
	}

	green, err := AnalyzeCommit(d, sha(2), "master", at(7))
	if err != nil || !strings.Contains(green[0].Text(), "No failing jobs at 00000000") {
		t.Errorf("green commit: %v %v", err, green)
	}
}

// headings is the report's skeleton: labels and section names, without values.
func headings(report string) string {
	var out []string
	for _, line := range strings.Split(report, "\n") {
		if label, _, ok := strings.Cut(line, ":"); ok && len(label) < 16 && !strings.Contains(label, " ") || slices.Contains([]string{"Evidence", "Validate", "Proposed fix", "Failing jobs", "Blame window", "Specific change"}, line) {
			out = append(out, strings.TrimSpace(label))
		}
	}
	return strings.Join(out, ",")
}
