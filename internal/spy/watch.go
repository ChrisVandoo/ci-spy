package spy

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"
)

// Deps is what a pass needs. Tests swap in fake GitHub and Anthropic servers.
type Deps struct {
	GH       *GitHub
	State    *State
	Config   Config
	Analyzer *Analyzer // nil: no analysis
	Out      io.Writer // where reports are printed
	Log      *log.Logger
	DryRun   bool
}

type PassResult struct {
	Branches []string
	Opened   []int
	Attached []int
	Resolved []int
	Notified []int
	Requests int
}

// Pass is one watch iteration: collect every watched branch, update incidents,
// send whatever notifications are owed.
func Pass(d Deps, now time.Time) (PassResult, error) {
	cfg := d.Config
	var res PassResult
	branches, err := ResolveBranches(d.GH, cfg)
	if err != nil {
		return res, err
	}
	res.Branches = branches
	snaps := map[string]BranchSnapshot{}
	var failingAtHead []BranchKeys

	for _, branch := range branches {
		snap, err := CollectBranch(d.GH, d.State, cfg, branch, branch, now)
		if err != nil {
			return res, fmt.Errorf("%s: %w", branch, err)
		}
		snaps[branch] = snap
		breakages := FindBreakages(snap, cfg.Detection)
		var keys []string
		for _, b := range breakages {
			keys = append(keys, b.Key)
		}
		failingAtHead = append(failingAtHead, BranchKeys{Branch: branch, Keys: keys})

		plan := Reconcile(d.State.OpenIncidents(cfg.Repo, branch), GroupBreakages(snap, breakages))
		for _, g := range plan.Open {
			inc := d.State.Open(cfg.Repo, branch, g, now)
			res.Opened = append(res.Opened, inc.ID)
			d.Log.Printf("%s: opened incident #%d (%s, first red %s)", branch, inc.ID, plural(len(g.Keys), "job"), short(g.FirstBad))
		}
		for _, a := range plan.Attach {
			a.Incident.Keys, a.Incident.FailingSHAs, a.Incident.UpdatedAt = a.Keys, a.FailingSHAs, now
			res.Attached = append(res.Attached, a.Incident.ID)
			d.Log.Printf("%s: incident #%d now has %s", branch, a.Incident.ID, plural(len(a.FailingSHAs), "red commit"))
		}
		for _, inc := range plan.Resolve {
			t := now
			inc.ResolvedAt, inc.UpdatedAt = &t, now
			res.Resolved = append(res.Resolved, inc.ID)
			d.Log.Printf("%s: incident #%d resolved", branch, inc.ID)
		}
		if len(plan.Open)+len(plan.Attach)+len(plan.Resolve) == 0 {
			state := "green"
			if len(breakages) > 0 {
				state = plural(len(breakages), "job") + " red, already tracked or unconfirmed"
			}
			head := ""
			if len(snap.Commits) > 0 {
				head = short(snap.Commits[0].SHA)
			}
			d.Log.Printf("%s: %s (head %s)", branch, state, head)
		}
	}

	for _, inc := range d.State.PendingNotifications(cfg.Repo) {
		if inc.ResolvedAt != nil {
			errs := Notify(ResolvedReport(inc, now), cfg.Notify, d.Out, fmt.Sprintf("incident-%d-resolved", inc.ID), d.DryRun)
			logErrs(d.Log, errs)
			if !d.DryRun {
				t := now
				inc.ResolvedNotifiedAt = &t
			}
			continue
		}
		snap, ok := snaps[inc.Branch]
		if !ok {
			continue // branch no longer watched
		}
		report := investigate(d, inc, snap, failingAtHead, now, fmt.Sprintf("%s is red: %s failing", inc.Branch, plural(len(inc.Keys), "job")), true)
		logErrs(d.Log, Notify(report, cfg.Notify, d.Out, fmt.Sprintf("incident-%d", inc.ID), d.DryRun))
		if !d.DryRun {
			t := now
			inc.NotifiedAt = &t
		}
		res.Notified = append(res.Notified, inc.ID)
	}

	d.State.PruneCache(now)
	res.Requests = d.GH.Requests
	return res, d.State.Save()
}

// investigate gathers evidence, runs the analysis once (if allowed) and builds
// the report. Shared by watch and analyze so both print the same thing.
func investigate(d Deps, inc *Incident, snap BranchSnapshot, others []BranchKeys, now time.Time, title string, budgeted bool) Report {
	cfg := d.Config
	ev, err := BuildEvidence(d.GH, cfg, inc, snap, others)
	if err != nil {
		d.Log.Printf("evidence for %s failed: %v", short(inc.FirstBad), err)
		ev = nil
	}
	if inc.Analysis == nil && ev != nil {
		day := cfg.Schedule.LocalDay(now)
		switch {
		case !cfg.Analysis.Enabled:
			inc.AnalysisError = "analysis disabled in config"
		case d.Analyzer == nil:
			inc.AnalysisError = "no analysis (ANTHROPIC_API_KEY is not set, or --no-ai)"
		case budgeted && d.State.Budget[day] >= cfg.Analysis.MaxPerDay:
			inc.AnalysisError = fmt.Sprintf("daily analysis budget (%d) reached", cfg.Analysis.MaxPerDay)
		default:
			d.State.Budget[day]++
			if a, err := d.Analyzer.Analyze(ev); err != nil {
				inc.AnalysisError = err.Error()
			} else {
				inc.Analysis, inc.AnalysisError = a, ""
			}
		}
	}
	var person *Person
	if s := PickSuspect(ev, inc.Analysis); s != nil {
		login := s.AuthorLogin
		if s.PR != nil && s.PR.Author != "" {
			login = s.PR.Author // the PR author, not whoever merged it
		}
		p := ResolvePerson(login, s.AuthorName, s.AuthorEmail, cfg.People, d.State)
		person = &p
	}
	return BuildReport(title, inc, ev, person, now, cfg.Notify.CIConductorURL)
}

func logErrs(l *log.Logger, errs []error) {
	for _, e := range errs {
		l.Printf("notify: %v", e)
	}
}

// Watch runs Pass every interval while inside the active window, until ctx is
// cancelled. With once, it runs a single pass (for cron/launchd).
func Watch(ctx context.Context, d Deps, once, force bool) error {
	interval := d.Config.Schedule.IntervalDuration()
	for {
		now := time.Now()
		if force || d.Config.Schedule.InActiveWindow(now) {
			res, err := Pass(d, now)
			if err != nil {
				d.Log.Printf("pass failed: %v", err)
			} else {
				d.Log.Printf("checked %v · opened %d, notified %d, resolved %d · %d GitHub requests",
					res.Branches, len(res.Opened), len(res.Notified), len(res.Resolved), res.Requests)
			}
			d.GH.Requests = 0
		} else {
			d.Log.Printf("outside the active window (%s %s-%s); not checking", d.Config.Schedule.Days, d.Config.Schedule.Start, d.Config.Schedule.End)
		}
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
