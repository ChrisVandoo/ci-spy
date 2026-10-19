package spy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// Deps is what a pass needs. Tests swap in fake GitHub and Anthropic servers.
type Deps struct {
	GH       *GitHub
	State    *State
	Config   Config
	Analyzer *Analyzer // nil: no analysis
	Out      io.Writer // where reports are printed
	Log      *slog.Logger
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
	d.Log.Info("starting watch pass", "repo", cfg.Repo)
	var res PassResult
	branches, err := ResolveBranches(d.GH, cfg)
	if err != nil {
		return res, err
	}
	res.Branches = branches
	d.Log.Info("resolved watched branches", "branches", branches)
	snaps := map[string]BranchSnapshot{}
	var failingAtHead []BranchKeys

	for _, branch := range branches {
		d.Log.Info("collecting CI status", "branch", branch)
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
			d.Log.Info("opened incident", "branch", branch, "incident", inc.ID, "jobs", len(g.Keys), "first_bad", short(g.FirstBad))
		}
		for _, a := range plan.Attach {
			a.Incident.Keys, a.Incident.FailingSHAs, a.Incident.UpdatedAt = a.Keys, a.FailingSHAs, now
			res.Attached = append(res.Attached, a.Incident.ID)
			d.Log.Info("updated incident", "branch", branch, "incident", a.Incident.ID, "red_commits", len(a.FailingSHAs))
		}
		for _, inc := range plan.Resolve {
			t := now
			inc.ResolvedAt, inc.UpdatedAt = &t, now
			res.Resolved = append(res.Resolved, inc.ID)
			d.Log.Info("resolved incident", "branch", branch, "incident", inc.ID)
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
			d.Log.Info("branch checked", "branch", branch, "status", state, "head", head)
		}
	}

	for _, inc := range d.State.PendingNotifications(cfg.Repo) {
		if inc.ResolvedAt != nil {
			d.Log.Info("reporting recovery", "incident", inc.ID, "dry_run", d.DryRun)
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
		d.Log.Info("reporting incident", "incident", inc.ID, "dry_run", d.DryRun)
		logErrs(d.Log, Notify(report, cfg.Notify, d.Out, fmt.Sprintf("incident-%d", inc.ID), d.DryRun))
		if !d.DryRun {
			t := now
			inc.NotifiedAt = &t
		}
		res.Notified = append(res.Notified, inc.ID)
	}

	d.State.PruneCache(now)
	res.Requests = d.GH.Requests
	if err := d.State.Save(); err != nil {
		return res, err
	}
	d.Log.Info("watch pass completed", "branches", res.Branches, "opened", len(res.Opened), "notified", len(res.Notified), "resolved", len(res.Resolved), "requests", res.Requests)
	return res, nil
}

// investigate gathers evidence, runs the analysis once (if allowed) and builds
// the report. Shared by watch and analyze so both print the same thing.
func investigate(d Deps, inc *Incident, snap BranchSnapshot, others []BranchKeys, now time.Time, title string, budgeted bool) Report {
	cfg := d.Config
	d.Log.Info("gathering evidence", "branch", inc.Branch, "first_bad", short(inc.FirstBad), "jobs", len(inc.Keys))
	ev, err := BuildEvidence(d.GH, cfg, inc, snap, others)
	if err != nil {
		d.Log.Warn("gathering evidence failed", "first_bad", short(inc.FirstBad), "error", err)
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
			d.Log.Info("analyzing evidence", "model", cfg.Analysis.Model, "first_bad", short(inc.FirstBad))
			d.State.Budget[day]++
			if a, err := d.Analyzer.Analyze(ev); err != nil {
				d.Log.Warn("analysis failed", "error", err)
				inc.AnalysisError = err.Error()
			} else {
				inc.Analysis, inc.AnalysisError = a, ""
				d.Log.Info("analysis completed", "first_bad", short(inc.FirstBad))
			}
		}
		if inc.Analysis == nil && inc.AnalysisError != "" {
			d.Log.Info("analysis unavailable", "reason", inc.AnalysisError)
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

func logErrs(l *slog.Logger, errs []error) {
	for _, e := range errs {
		l.Warn("notification failed", "error", e)
	}
}

// Watch runs Pass every interval while inside the active window, until ctx is
// cancelled. With once, it runs a single pass (for cron/launchd).
func Watch(ctx context.Context, d Deps, once, force bool) error {
	interval := d.Config.Schedule.IntervalDuration()
	d.Log.Info("watch started", "interval", interval, "once", once, "force", force, "dry_run", d.DryRun)
	defer d.Log.Info("watch stopped")
	for {
		now := time.Now()
		if force || d.Config.Schedule.InActiveWindow(now) {
			_, err := Pass(d, now)
			if err != nil {
				d.Log.Error("watch pass failed", "error", err)
			}
			d.GH.Requests = 0
		} else {
			d.Log.Info("outside active window; skipping check", "days", d.Config.Schedule.Days, "start", d.Config.Schedule.Start, "end", d.Config.Schedule.End)
		}
		if once {
			return nil
		}
		d.Log.Info("waiting for next check", "interval", interval)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
