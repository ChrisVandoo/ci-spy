package spy

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// A scenario-driven stand-in for the GitHub REST API so tests exercise the real
// collector, evidence builder and passes end to end.

type fakeJob struct {
	Name       string
	Conclusion string // "" = still running
	Attempt    int
	Log        string
}

type fakeRun struct {
	Workflow   string
	Conclusion string
	Attempt    int
	Jobs       []fakeJob
}

type fakeCommit struct {
	SHA     string
	Login   string
	Name    string
	At      string
	Message string
	PR      *PR
	Files   []ChangedFile
	Runs    []fakeRun
}

type fakeGitHub struct {
	mu       sync.Mutex
	repo     string
	branches map[string][]fakeCommit // newest first
	calls    []string
}

// Ids derive from content so a commit keeps its run ids across scenarios, as on
// GitHub (the collector caches by run id).
func fakeID(s string) int64 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return int64(h.Sum32() % 1_000_000_000)
}

func (f *fakeGitHub) all() []fakeCommit {
	var out []fakeCommit
	for _, cs := range f.branches {
		out = append(out, cs...)
	}
	return out
}

func (f *fakeGitHub) find(sha string) *fakeCommit {
	for _, c := range f.all() {
		if strings.HasPrefix(c.SHA, sha) {
			c := c
			return &c
		}
	}
	return nil
}

func (f *fakeGitHub) commitJSON(c fakeCommit) map[string]any {
	return map[string]any{
		"sha":      c.SHA,
		"html_url": "https://github.com/" + f.repo + "/commit/" + c.SHA,
		"author":   map[string]any{"login": c.Login},
		"commit": map[string]any{
			"message":   orDefault(c.Message, "change by "+c.Login),
			"author":    map[string]any{"name": orDefault(c.Name, c.Login), "email": c.Login + "@example.com", "date": c.At},
			"committer": map[string]any{"date": c.At},
		},
	}
}

func status(conclusion string) string {
	if conclusion == "" {
		return "in_progress"
	}
	return "completed"
}

var (
	reRefs   = regexp.MustCompile(`/git/matching-refs/heads/`)
	reList   = regexp.MustCompile(`/commits$`)
	reRuns   = regexp.MustCompile(`/actions/runs$`)
	reJobs   = regexp.MustCompile(`/actions/runs/(\d+)/jobs$`)
	reLogs   = regexp.MustCompile(`/actions/jobs/(\d+)/logs$`)
	rePulls  = regexp.MustCompile(`/commits/([0-9a-f]{40})/pulls$`)
	reCommit = regexp.MustCompile(`/commits/([0-9a-f]{40})$`)
)

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.RequestURI())
	f.mu.Unlock()
	p, q := r.URL.Path, r.URL.Query()
	write := func(v any) { json.NewEncoder(w).Encode(v) }
	runID := func(c fakeCommit, run fakeRun) int64 { return fakeID(c.SHA + ":" + run.Workflow) }
	jobID := func(c fakeCommit, run fakeRun, j fakeJob) int64 {
		return fakeID(c.SHA + ":" + run.Workflow + ":" + j.Name)
	}
	attempt := func(n int) int { return max(1, n) }

	switch {
	case reRefs.MatchString(p):
		write([]any{})
	case reList.MatchString(p):
		n, _ := strconv.Atoi(q.Get("per_page"))
		ref := q.Get("sha")
		commits, ok := f.branches[ref]
		if !ok { // a sha: history ending at that commit
			for _, cs := range f.branches {
				for i, c := range cs {
					if strings.HasPrefix(c.SHA, ref) {
						commits = cs[i:]
					}
				}
			}
		}
		var out []any
		for i, c := range commits {
			if i < n {
				out = append(out, f.commitJSON(c))
			}
		}
		write(out)
	case reRuns.MatchString(p):
		c := f.find(q.Get("head_sha"))
		var runs []any
		if c != nil && q.Get("page") == "1" {
			for _, run := range c.Runs {
				runs = append(runs, map[string]any{
					"id": runID(*c, run), "name": run.Workflow, "head_sha": c.SHA, "status": status(run.Conclusion),
					"conclusion": run.Conclusion, "run_attempt": attempt(run.Attempt), "event": "push", "created_at": c.At,
					"html_url": fmt.Sprintf("https://github.com/%s/actions/runs/%d", f.repo, runID(*c, run)),
				})
			}
		}
		write(map[string]any{"workflow_runs": runs})
	case reJobs.MatchString(p):
		id, _ := strconv.ParseInt(reJobs.FindStringSubmatch(p)[1], 10, 64)
		for _, c := range f.all() {
			for _, run := range c.Runs {
				if runID(c, run) != id {
					continue
				}
				var jobs []any
				for _, j := range run.Jobs {
					completed := ""
					if j.Conclusion != "" {
						completed = c.At
					}
					jobs = append(jobs, map[string]any{
						"id": jobID(c, run, j), "run_id": id, "run_attempt": attempt(max(j.Attempt, run.Attempt)), "name": j.Name,
						"status": status(j.Conclusion), "conclusion": j.Conclusion, "started_at": c.At, "completed_at": completed,
						"html_url": fmt.Sprintf("https://github.com/%s/actions/runs/%d/job/%d", f.repo, id, jobID(c, run, j)),
					})
				}
				write(map[string]any{"jobs": jobs})
				return
			}
		}
		http.NotFound(w, r)
	case reLogs.MatchString(p):
		id, _ := strconv.ParseInt(reLogs.FindStringSubmatch(p)[1], 10, 64)
		for _, c := range f.all() {
			for _, run := range c.Runs {
				for _, j := range run.Jobs {
					if jobID(c, run, j) == id {
						fmt.Fprint(w, orDefault(j.Log, "no log"))
						return
					}
				}
			}
		}
		http.NotFound(w, r)
	case rePulls.MatchString(p):
		c := f.find(rePulls.FindStringSubmatch(p)[1])
		out := []any{}
		if c != nil && c.PR != nil {
			out = append(out, map[string]any{"number": c.PR.Number, "title": c.PR.Title, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", f.repo, c.PR.Number), "body": c.PR.Body, "user": map[string]any{"login": c.PR.Author}, "merged_at": c.At})
		}
		write(out)
	case reCommit.MatchString(p):
		c := f.find(reCommit.FindStringSubmatch(p)[1])
		if c == nil {
			http.NotFound(w, r)
			return
		}
		body := f.commitJSON(*c)
		var files []any
		for _, file := range c.Files {
			files = append(files, map[string]any{"filename": file.Filename, "status": "modified", "additions": 3, "deletions": 1, "patch": file.Patch})
		}
		body["files"] = files
		write(body)
	default:
		http.Error(w, "unrouted "+p, http.StatusNotFound)
	}
}

func newFakeGitHub(repo string, master []fakeCommit) (*fakeGitHub, *GitHub, func()) {
	f := &fakeGitHub{repo: repo, branches: map[string][]fakeCommit{"master": master}}
	srv := httptest.NewServer(f)
	gh := &GitHub{BaseURL: srv.URL, HTTP: srv.Client()}
	return f, gh, srv.Close
}

// fakeAnthropic answers every request with the given analysis as a tool call
// and records the user message it was sent.
func fakeAnthropic(a Analysis, prompts *[]string) (*Analyzer, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			ToolChoice struct {
				Name string `json:"name"`
			} `json:"tool_choice"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		*prompts = append(*prompts, req.Messages[0].Content)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "name": req.ToolChoice.Name, "input": a}},
			"usage":   map[string]any{"input_tokens": 1000, "output_tokens": 200},
		})
	}))
	return &Analyzer{URL: srv.URL, APIKey: "k", Model: "test-model", HTTP: srv.Client()}, srv.Close
}
