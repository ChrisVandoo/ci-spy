package spy

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Everything the analysis (and you) need to judge a breakage: the commits in the
// blame window with their PRs and diffs, and log excerpts from the failing jobs.
// Runs once per incident, so it can afford more requests than the collector.

const maxPatchChars = 8_000

type ghCommitDetail struct {
	ghCommit
	Files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Patch     string `json:"patch"`
	} `json:"files"`
}

type ghPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	User    *struct {
		Login string `json:"login"`
	} `json:"user"`
	MergedAt *string `json:"merged_at"`
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// Words that show up in most paths or logs and say nothing about which file
// broke the build.
var stopTokens = map[string]bool{
	"test": true, "tests": true, "spec": true, "main": true, "java": true, "clojure": true,
	"yaml": true, "json": true, "index": true, "utils": true, "frontend": true, "backend": true,
	"resources": true, "enterprise": true, "shared": true, "components": true, "error": true,
	"errors": true, "failed": true, "failure": true, "exit": true, "code": true, "process": true,
	"completed": true, "running": true, "starting": true, "expected": true, "actual": true,
}

func tokens(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range nonWord.Split(strings.ToLower(text), -1) {
		if len(t) >= 4 && strings.Trim(t, "0123456789") != "" {
			out[t] = true
		}
	}
	return out
}

// rankFiles orders changed files by how much their path overlaps with what is
// failing, so the diff budget goes to the hunks most likely to matter.
func rankFiles(files []ChangedFile, signal map[string]bool) []ChangedFile {
	score := func(f ChangedFile) int {
		n := 0
		for t := range tokens(f.Filename) {
			if signal[t] {
				n++
			}
		}
		return n
	}
	out := slices.Clone(files)
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := score(out[i]), score(out[j])
		if si != sj {
			return si > sj
		}
		return out[i].Additions+out[i].Deletions > out[j].Additions+out[j].Deletions
	})
	return out
}

func jobEvidence(gh *GitHub, cfg Config, inc *Incident, snap BranchSnapshot) []FailingJobEvidence {
	var out []FailingJobEvidence
	seen := map[string]bool{}
	// Prefer logs from the first bad commit: that is where the cause is freshest.
	for _, sha := range append([]string{inc.FirstBad}, inc.FailingSHAs...) {
		for _, c := range snap.Commits {
			if c.SHA != sha {
				continue
			}
			for _, j := range c.Jobs {
				if len(out) >= cfg.Analysis.MaxJobLogs {
					return out
				}
				if !slices.Contains(inc.Keys, j.Key) || seen[j.Key] || !failed[j.Conclusion] {
					continue
				}
				seen[j.Key] = true
				ev := FailingJobEvidence{Key: j.Key, URL: j.URL, Conclusion: j.Conclusion}
				raw, err := gh.Text(fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", cfg.Repo, j.ID))
				if err != nil {
					ev.Excerpt = "(log unavailable: " + err.Error() + ")"
				} else {
					lines := CleanLog(raw)
					ev.FailingTests = FailingTests(lines, 25)
					ev.Excerpt = Excerpt(lines, cfg.Analysis.LogExcerptLines, 3, 15)
				}
				out = append(out, ev)
			}
		}
	}
	return out
}

func BuildEvidence(gh *GitHub, cfg Config, inc *Incident, snap BranchSnapshot, others []BranchKeys) (*Evidence, error) {
	shas := make([]string, len(snap.Commits))
	for i, c := range snap.Commits {
		shas[i] = c.SHA
	}
	f := slices.Index(shas, inc.FirstBad)
	g := -1
	if inc.LastGood != "" {
		g = slices.Index(shas, inc.LastGood)
	}
	// Blame window: the first bad commit plus anything between it and the last
	// green (commits where the job did not run or is still pending). Newest
	// first, so truncating keeps the first bad commit itself.
	window := []string{inc.FirstBad}
	if f >= 0 {
		end := f + 1
		if g > f {
			end = g
		}
		window = shas[f:end]
	}
	truncated := len(window) > cfg.Analysis.MaxSuspectCommits
	if truncated {
		window = window[:cfg.Analysis.MaxSuspectCommits]
	}

	jobs := jobEvidence(gh, cfg, inc, snap)
	var sig strings.Builder
	sig.WriteString(strings.Join(inc.Keys, " "))
	for _, j := range jobs {
		sig.WriteString(" " + strings.Join(j.FailingTests, " ") + " " + j.Excerpt)
	}
	signal := tokens(sig.String())
	for t := range tokens(cfg.Repo) {
		delete(signal, t)
	}
	for t := range stopTokens {
		delete(signal, t)
	}

	budget := cfg.Analysis.DiffCharBudget
	var suspects []SuspectCommit
	for _, sha := range window {
		var d ghCommitDetail
		if err := gh.JSON(fmt.Sprintf("/repos/%s/commits/%s", cfg.Repo, sha), &d); err != nil {
			return nil, err
		}
		s := SuspectCommit{CommitInfo: d.info()}
		var pulls []ghPull
		// Not every commit has a PR (direct pushes, bots), so errors are fine.
		if gh.JSON(fmt.Sprintf("/repos/%s/commits/%s/pulls", cfg.Repo, sha), &pulls) == nil && len(pulls) > 0 {
			p := pulls[0]
			for _, x := range pulls {
				if x.MergedAt != nil {
					p = x
					break
				}
			}
			body := p.Body
			if len(body) > 3000 {
				body = body[:3000]
			}
			author := ""
			if p.User != nil {
				author = p.User.Login
			}
			s.PR = &PR{Number: p.Number, Title: p.Title, URL: p.HTMLURL, Body: body, Author: author}
		}
		files := make([]ChangedFile, len(d.Files))
		for i, x := range d.Files {
			files[i] = ChangedFile{Filename: x.Filename, Status: x.Status, Additions: x.Additions, Deletions: x.Deletions, Patch: x.Patch}
		}
		for _, file := range rankFiles(files, signal) {
			patch := file.Patch
			if len(patch) > maxPatchChars {
				patch = patch[:maxPatchChars]
			}
			if patch != "" && len(patch) <= budget {
				budget -= len(patch)
				file.Patch = patch
			} else {
				file.Patch = ""
			}
			s.Files = append(s.Files, file)
		}
		if len(s.Files) > 60 {
			s.OmittedFiles = len(s.Files) - 60
			s.Files = s.Files[:60]
		}
		suspects = append(suspects, s)
	}

	ev := &Evidence{
		Repo:              cfg.Repo,
		Branch:            inc.Branch,
		Keys:              inc.Keys,
		RedCommits:        len(inc.FailingSHAs),
		FirstFailedAt:     inc.FirstFailedAt,
		Suspects:          suspects,
		SuspectsTruncated: truncated,
		Jobs:              jobs,
	}
	for _, s := range suspects {
		if s.SHA == inc.FirstBad {
			ev.FirstBad = s.CommitInfo
		}
	}
	for _, c := range snap.Commits {
		if c.SHA == inc.LastGood {
			lg := c.CommitInfo
			ev.LastGood = &lg
		}
	}
	for _, o := range others {
		if o.Branch != inc.Branch && intersects(o.Keys, inc.Keys) {
			ev.OtherBranches = append(ev.OtherBranches, o)
		}
	}
	return ev, nil
}
