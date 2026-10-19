package spy

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// `analyze <commit>`: point at a red commit and get the same report watch would
// send, without touching watch's incidents. The commit is treated as the head:
// any job red there counts, with no need for a second red commit.

var commitRef = regexp.MustCompile(`(?:/commit/|^)([0-9a-fA-F]{7,40})(?:[/?#].*)?$`)

// ParseCommitRef accepts a sha (short or full) or a GitHub commit URL.
func ParseCommitRef(ref string) (string, error) {
	m := commitRef.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", fmt.Errorf("%q is not a commit sha or GitHub commit URL", ref)
	}
	return strings.ToLower(m[1]), nil
}

func AnalyzeCommit(d Deps, ref, branch string, now time.Time) ([]Report, error) {
	cfg := d.Config
	sha, err := ParseCommitRef(ref)
	if err != nil {
		return nil, err
	}
	d.Log.Info("collecting CI status", "repo", cfg.Repo, "commit", sha, "branch", branch)
	snap, err := CollectBranch(d.GH, d.State, cfg, sha, branch, now)
	if err != nil {
		return nil, err
	}
	if len(snap.Commits) == 0 {
		return nil, fmt.Errorf("commit %s not found in %s", sha, cfg.Repo)
	}
	head := snap.Commits[0]
	detection := cfg.Detection
	detection.MinConsecutiveFailures = 1
	groups := GroupBreakages(snap, FindBreakages(snap, detection))
	d.Log.Info("CI status collected", "commits", len(snap.Commits), "failure_groups", len(groups))

	var reports []Report
	for _, g := range groups {
		inc := &Incident{
			Repo:          cfg.Repo,
			Branch:        branch,
			Keys:          g.Keys,
			FirstBad:      g.FirstBad,
			LastGood:      g.LastGood,
			FailingSHAs:   g.FailingSHAs,
			FirstFailedAt: g.FirstFailedAt,
			OpenedAt:      now,
		}
		title := fmt.Sprintf("%s on %s: %s failing", short(head.SHA), branch, plural(len(g.Keys), "job"))
		reports = append(reports, investigate(d, inc, snap, nil, now, title, false))
	}
	if len(reports) == 0 {
		pendingNote := ""
		for _, w := range head.Workflows {
			if w.Status != "completed" {
				pendingNote = " Some workflows are still running."
				break
			}
		}
		msg := fmt.Sprintf("No failing jobs at %s (%s).%s", short(head.SHA), firstLine(head.Message), pendingNote)
		reports = append(reports, Report{Title: short(head.SHA) + " on " + branch + ": nothing failing", Note: msg, Short: msg})
	}
	if err := d.State.Save(); err != nil {
		return reports, err
	}
	d.Log.Info("commit analysis completed", "commit", short(head.SHA), "reports", len(reports), "requests", d.GH.Requests)
	return reports, nil
}
