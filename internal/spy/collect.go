package spy

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Collector: GitHub Actions state for a branch, as a BranchSnapshot.
//
// Cost control is the point of the shape. Per pass and branch: one commits
// call, one runs call per commit that has not settled (settled commits come
// from the cache), and job listings only for workflows that failed somewhere
// in the window. A green branch costs a couple of requests.

// A commit's runs stop changing once they have all completed and the usual
// re-run window has passed.
const settleAfter = 6 * time.Hour

type ghCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Author  *struct {
		Login string `json:"login"`
	} `json:"author"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

func (c ghCommit) info() CommitInfo {
	login := ""
	if c.Author != nil {
		login = c.Author.Login
	}
	return CommitInfo{
		SHA:         c.SHA,
		Message:     c.Commit.Message,
		AuthorName:  c.Commit.Author.Name,
		AuthorEmail: c.Commit.Author.Email,
		AuthorLogin: login,
		CommittedAt: c.Commit.Committer.Date,
		URL:         c.HTMLURL,
	}
}

type ghRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	RunAttempt int    `json:"run_attempt"`
	HTMLURL    string `json:"html_url"`
	Event      string `json:"event"`
	CreatedAt  string `json:"created_at"`
}

type ghJob struct {
	ID          int64  `json:"id"`
	RunID       int64  `json:"run_id"`
	RunAttempt  int    `json:"run_attempt"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	HTMLURL     string `json:"html_url"`
}

var digits = regexp.MustCompile(`\d+`)

// compareVersionDesc sorts release-x.58.x before release-x.9.x.
func compareVersionDesc(a, b string) bool {
	na, nb := digits.FindAllString(a, -1), digits.FindAllString(b, -1)
	for i := 0; i < max(len(na), len(nb)); i++ {
		x, y := -1, -1
		if i < len(na) {
			x, _ = strconv.Atoi(na[i])
		}
		if i < len(nb) {
			y, _ = strconv.Atoi(nb[i])
		}
		if x != y {
			return x > y
		}
	}
	return a > b
}

func globMatch(glob, s string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(glob), `\*`, ".*") + "$"
	ok, _ := regexp.MatchString(re, s)
	return ok
}

// ResolveBranches expands globs to the newest ReleaseBranchLimit matches.
func ResolveBranches(gh *GitHub, cfg Config) ([]string, error) {
	var out []string
	for _, pattern := range cfg.Branches {
		star := strings.Index(pattern, "*")
		if star < 0 {
			out = append(out, pattern)
			continue
		}
		var refs []struct {
			Ref string `json:"ref"`
		}
		if err := gh.JSON(fmt.Sprintf("/repos/%s/git/matching-refs/heads/%s", cfg.Repo, pattern[:star]), &refs); err != nil {
			return nil, err
		}
		var names []string
		for _, r := range refs {
			name := strings.TrimPrefix(r.Ref, "refs/heads/")
			if globMatch(pattern, name) {
				names = append(names, name)
			}
		}
		sort.Slice(names, func(i, j int) bool { return compareVersionDesc(names[i], names[j]) })
		if len(names) > cfg.ReleaseBranchLimit {
			names = names[:cfg.ReleaseBranchLimit]
		}
		out = append(out, names...)
	}
	return slices.Compact(out), nil
}

// latestPerWorkflow keeps the newest run per workflow (one can be triggered twice).
func latestPerWorkflow(runs []ghRun) []ghRun {
	byName := map[string]ghRun{}
	var order []string
	for _, r := range runs {
		prev, ok := byName[r.Name]
		if !ok {
			order = append(order, r.Name)
		}
		if !ok || r.CreatedAt > prev.CreatedAt {
			byName[r.Name] = r
		}
	}
	out := make([]ghRun, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

// CollectBranch snapshots the commitWindow commits ending at ref (a branch name
// or a sha). label is the branch name the snapshot is reported under.
func CollectBranch(gh *GitHub, st *State, cfg Config, ref, label string, now time.Time) (BranchSnapshot, error) {
	repo := cfg.Repo
	var commits []ghCommit
	if err := gh.JSON(fmt.Sprintf("/repos/%s/commits?sha=%s&per_page=%d", repo, url.QueryEscape(ref), cfg.CommitWindow), &commits); err != nil {
		return BranchSnapshot{}, err
	}

	runsBySHA := map[string][]ghRun{}
	for _, c := range commits {
		key := "runs:" + repo + ":" + c.SHA
		var runs []ghRun
		if !st.CacheGet(key, &runs) {
			path := fmt.Sprintf("/repos/%s/actions/runs?head_sha=%s", repo, c.SHA)
			if len(cfg.Events) == 1 {
				path += "&event=" + cfg.Events[0]
			}
			fetched, err := Paginate[ghRun](gh, path, "workflow_runs", 5)
			if err != nil {
				return BranchSnapshot{}, err
			}
			runs = nil
			for _, r := range latestPerWorkflow(fetched) {
				if slices.Contains(cfg.Events, r.Event) {
					runs = append(runs, r)
				}
			}
			settled := len(runs) > 0
			for _, r := range runs {
				settled = settled && r.Status == "completed"
			}
			committed, _ := time.Parse(time.RFC3339, c.Commit.Committer.Date)
			if settled && now.Sub(committed) > settleAfter {
				st.CacheSet(key, runs)
			}
		}
		runsBySHA[c.SHA] = runs
	}

	// Workflows worth looking inside: any that failed somewhere in the window.
	interesting := map[string]bool{}
	for _, runs := range runsBySHA {
		for _, r := range runs {
			if failed[r.Conclusion] {
				interesting[r.Name] = true
			}
		}
	}

	snap := BranchSnapshot{Repo: repo, Branch: label, TakenAt: now}
	for _, c := range commits {
		cs := CommitSnapshot{CommitInfo: c.info()}
		for _, run := range runsBySHA[c.SHA] {
			fetch := interesting[run.Name]
			cs.Workflows = append(cs.Workflows, WorkflowRun{
				ID: run.ID, Workflow: run.Name, Status: run.Status, Conclusion: run.Conclusion,
				RunAttempt: run.RunAttempt, URL: run.HTMLURL, JobsFetched: fetch,
			})
			if !fetch {
				continue
			}
			key := fmt.Sprintf("jobs:%s:%d:%d", repo, run.ID, run.RunAttempt)
			var jobs []ghJob
			if !st.CacheGet(key, &jobs) {
				var err error
				jobs, err = Paginate[ghJob](gh, fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=latest", repo, run.ID), "jobs", 5)
				if err != nil {
					return BranchSnapshot{}, err
				}
				if run.Status == "completed" {
					st.CacheSet(key, jobs)
				}
			}
			for _, j := range jobs {
				cs.Jobs = append(cs.Jobs, JobRun{
					ID: j.ID, RunID: j.RunID, RunAttempt: j.RunAttempt, Workflow: run.Name, Name: j.Name,
					Key: JobKey(run.Name, j.Name), Status: j.Status, Conclusion: j.Conclusion,
					StartedAt: j.StartedAt, CompletedAt: j.CompletedAt, URL: j.HTMLURL,
				})
			}
		}
		snap.Commits = append(snap.Commits, cs)
	}
	return snap, nil
}
