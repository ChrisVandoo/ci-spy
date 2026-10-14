package spy

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Detection without I/O: which jobs are broken on a branch, since which commit,
// and how that lines up with incidents already known. This is the cheap dedup:
// a train of red commits behind one bad merge shares a first-bad commit and a
// set of job keys, so it attaches to the existing incident instead of
// triggering another analysis.

var shardPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\(\s*\d+\s*(?:/|of)\s*\d+\s*\)`),
	regexp.MustCompile(`\(\s*\d+\s*\)`),
	regexp.MustCompile(`(?i)\b(?:shard|part|chunk|group)[\s#_-]*\d+(?:\s*(?:/|of)\s*\d+)?`),
	regexp.MustCompile(`#\d+`),
}

var (
	emptyParens  = regexp.MustCompile(`\(\s*\)`)
	spaces       = regexp.MustCompile(`\s+`)
	trailingJunk = regexp.MustCompile(`[\s,/-]+$`)
)

// JobKey identifies a job across commits. Matrix shards fail on different shard
// numbers from one commit to the next, so the number is not part of it.
func JobKey(workflow, name string) string {
	n := name
	for _, re := range shardPatterns {
		n = re.ReplaceAllString(n, "")
	}
	n = emptyParens.ReplaceAllString(n, "")
	n = strings.TrimSpace(spaces.ReplaceAllString(n, " "))
	n = trailingJunk.ReplaceAllString(n, "")
	return workflow + " / " + n
}

type outcome int

const (
	unknown outcome = iota
	pending
	pass
	fail
)

func outcomeFor(c CommitSnapshot, key, workflow string) outcome {
	var jobs []JobRun
	for _, j := range c.Jobs {
		if j.Key == key {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) > 0 {
		anyPending, anyPass := false, false
		for _, j := range jobs {
			if j.Status == "completed" && failed[j.Conclusion] {
				return fail
			}
			if j.Status != "completed" {
				anyPending = true
			}
			if j.Conclusion == "success" {
				anyPass = true
			}
		}
		if anyPending {
			return pending
		}
		if anyPass {
			return pass
		}
		return unknown
	}
	// Jobs of workflows that never failed in the window aren't fetched; a green
	// run of the workflow stands in for its jobs.
	for _, w := range c.Workflows {
		if w.Workflow != workflow {
			continue
		}
		if w.JobsFetched {
			return unknown
		}
		if w.Status != "completed" {
			return pending
		}
		if w.Conclusion == "success" {
			return pass
		}
		return unknown
	}
	return unknown
}

// FindBreakages lists every job that is red at the branch head (ignoring
// pending commits), with its blame window.
func FindBreakages(snap BranchSnapshot, d DetectionConfig) []Breakage {
	var ignore []*regexp.Regexp
	for _, p := range d.IgnoreJobs {
		if re, err := regexp.Compile(p); err == nil {
			ignore = append(ignore, re)
		}
	}
	workflowOf := map[string]string{}
	var keys []string
	seen := map[string]bool{}
	for _, c := range snap.Commits {
		for _, j := range c.Jobs {
			workflowOf[j.Key] = j.Workflow
			if !failed[j.Conclusion] || seen[j.Key] {
				continue
			}
			if slices.ContainsFunc(ignore, func(re *regexp.Regexp) bool { return re.MatchString(j.Key) }) {
				continue
			}
			seen[j.Key] = true
			keys = append(keys, j.Key)
		}
	}
	sort.Strings(keys)

	var out []Breakage
	for _, key := range keys {
		outcomes := make([]outcome, len(snap.Commits))
		for i, c := range snap.Commits {
			outcomes[i] = outcomeFor(c, key, workflowOf[key])
		}
		var failing []string
		lastGood := -1
		for i, o := range outcomes {
			if o == pending || o == unknown {
				continue
			}
			if o == pass {
				lastGood = i
				break
			}
			failing = append(failing, snap.Commits[i].SHA)
		}
		// Head (ignoring pending) is green for this job: nothing broken now.
		if len(failing) == 0 {
			continue
		}

		flips := 0
		if lastGood >= 0 {
			prev := unknown
			for _, o := range outcomes[lastGood:] {
				if o != pass && o != fail {
					continue
				}
				if prev != unknown && o != prev {
					flips++
				}
				prev = o
			}
		}

		failingSet := map[string]bool{}
		for _, s := range failing {
			failingSet[s] = true
		}
		firstBad := failing[len(failing)-1]
		rerun := false
		firstFailedAt := ""
		for _, c := range snap.Commits {
			if !failingSet[c.SHA] {
				continue
			}
			for _, j := range c.Jobs {
				if j.Key != key || !failed[j.Conclusion] {
					continue
				}
				if j.RunAttempt > 1 {
					rerun = true
				}
				if c.SHA == firstBad && j.CompletedAt != "" && (firstFailedAt == "" || j.CompletedAt < firstFailedAt) {
					firstFailedAt = j.CompletedAt
				}
			}
		}
		required := d.MinConsecutiveFailures
		if d.FlakyFlips > 0 && flips >= d.FlakyFlips {
			required++
		}
		b := Breakage{
			Key:              key,
			FirstBad:         firstBad,
			FailingSHAs:      failing,
			FirstFailedAt:    firstFailedAt,
			Confirmed:        len(failing) >= required || rerun,
			ConfirmedByRerun: rerun,
			Flips:            flips,
		}
		if lastGood >= 0 {
			b.LastGood = snap.Commits[lastGood].SHA
		}
		out = append(out, b)
	}
	return out
}

type BreakageGroup struct {
	FirstBad      string
	LastGood      string
	Keys          []string
	FailingSHAs   []string
	FirstFailedAt string
	Confirmed     bool
}

// GroupBreakages: jobs that started failing on the same commit are one breakage.
func GroupBreakages(snap BranchSnapshot, bs []Breakage) []BreakageGroup {
	order := map[string]int{}
	for i, c := range snap.Commits {
		order[c.SHA] = i
	}
	var groups []*BreakageGroup
	byFirst := map[string]*BreakageGroup{}
	for _, b := range bs {
		g, ok := byFirst[b.FirstBad]
		if !ok {
			g = &BreakageGroup{FirstBad: b.FirstBad, LastGood: b.LastGood}
			byFirst[b.FirstBad] = g
			groups = append(groups, g)
		}
		g.Keys = append(g.Keys, b.Key)
		g.FailingSHAs = union(g.FailingSHAs, b.FailingSHAs)
		if b.FirstFailedAt != "" && (g.FirstFailedAt == "" || b.FirstFailedAt < g.FirstFailedAt) {
			g.FirstFailedAt = b.FirstFailedAt
		}
		g.Confirmed = g.Confirmed || b.Confirmed
		// Prefer the tightest blame window when keys disagree about the last green.
		if b.LastGood != "" && (g.LastGood == "" || order[b.LastGood] < order[g.LastGood]) {
			g.LastGood = b.LastGood
		}
	}
	out := make([]BreakageGroup, 0, len(groups))
	for _, g := range groups {
		sort.Strings(g.Keys)
		sort.SliceStable(g.FailingSHAs, func(a, b int) bool { return order[g.FailingSHAs[a]] < order[g.FailingSHAs[b]] })
		out = append(out, *g)
	}
	return out
}

type Attach struct {
	Incident    *Incident
	Keys        []string
	FailingSHAs []string
}

type Plan struct {
	Open    []BreakageGroup
	Attach  []Attach
	Resolve []*Incident
}

// Reconcile matches current breakages against open incidents.
func Reconcile(open []*Incident, groups []BreakageGroup) Plan {
	var plan Plan
	matched := map[int]bool{}
	for _, g := range groups {
		var hit *Incident
		for _, i := range open {
			if i.FirstBad == g.FirstBad || intersects(i.Keys, g.Keys) {
				hit = i
				break
			}
		}
		switch {
		case hit != nil:
			matched[hit.ID] = true
			keys := union(hit.Keys, g.Keys)
			sort.Strings(keys)
			failing := union(g.FailingSHAs, hit.FailingSHAs)
			if len(keys) != len(hit.Keys) || len(failing) != len(hit.FailingSHAs) {
				plan.Attach = append(plan.Attach, Attach{Incident: hit, Keys: keys, FailingSHAs: failing})
			}
		case g.Confirmed:
			plan.Open = append(plan.Open, g)
		}
	}
	// An open incident none of whose jobs is red at head any more is over.
	for _, i := range open {
		if !matched[i.ID] {
			plan.Resolve = append(plan.Resolve, i)
		}
	}
	return plan
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}
