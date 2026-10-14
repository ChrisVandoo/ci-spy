package spy

import (
	"fmt"
	"strings"
	"time"
)

// One report, three renderings: the terminal (what both `watch` and `analyze`
// print), Markdown (saved under reports/) and Slack mrkdwn (the ping). They are
// built from the same Report so the modes can't drift apart.

type ReportJob struct {
	Key   string
	URL   string
	Tests []string
}

type Report struct {
	Title         string
	Short         string // one line for the desktop notification
	Repo          string
	BrokenFor     string
	RedCommits    int
	Suspect       *SuspectCommit
	Person        *Person
	Analysis      *Analysis
	AnalysisError string
	Jobs          []ReportJob
	FirstBad      string
	LastGood      string
	OtherBranches []string
	CIConductor   string
	Footer        string
	// A plain message instead of the full layout (recoveries, green commits).
	Note string
}

func Since(from string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return "unknown"
	}
	mins := max(0, int(now.Sub(t).Round(time.Minute).Minutes()))
	d, h, m := mins/1440, (mins%1440)/60, mins%60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// PickSuspect: the analysis's pick, or the only commit in the blame window.
func PickSuspect(ev *Evidence, a *Analysis) *SuspectCommit {
	if ev == nil {
		return nil
	}
	if a != nil && a.Suspect.SHA != "" {
		for i, s := range ev.Suspects {
			if strings.HasPrefix(s.SHA, a.Suspect.SHA) || strings.HasPrefix(a.Suspect.SHA, s.SHA) {
				return &ev.Suspects[i]
			}
		}
		return nil
	}
	if a != nil {
		return nil // the analysis deliberately named no suspect
	}
	if len(ev.Suspects) == 1 {
		return &ev.Suspects[0]
	}
	return nil
}

// BuildReport assembles the report for an incident. title is supplied by the
// caller: watch says "<branch> is red", analyze names the commit asked about.
func BuildReport(title string, inc *Incident, ev *Evidence, person *Person, now time.Time, ciConductorURL string) Report {
	r := Report{
		Title:         title,
		Repo:          inc.Repo,
		BrokenFor:     Since(orDefault(inc.FirstFailedAt, inc.OpenedAt.Format(time.RFC3339)), now),
		RedCommits:    len(inc.FailingSHAs),
		Suspect:       PickSuspect(ev, inc.Analysis),
		Person:        person,
		Analysis:      inc.Analysis,
		AnalysisError: inc.AnalysisError,
		FirstBad:      inc.FirstBad,
		LastGood:      inc.LastGood,
	}
	for _, k := range inc.Keys {
		j := ReportJob{Key: k}
		if ev != nil {
			for _, e := range ev.Jobs {
				if e.Key == k {
					j.URL, j.Tests = e.URL, e.FailingTests
				}
			}
		}
		r.Jobs = append(r.Jobs, j)
	}
	if ev != nil {
		for _, o := range ev.OtherBranches {
			r.OtherBranches = append(r.OtherBranches, o.Branch)
		}
	}
	if ciConductorURL != "" {
		r.CIConductor = strings.TrimRight(ciConductorURL, "/") + "/commits/" + inc.FirstBad
	}
	var bits []string
	if r.Suspect != nil && r.Suspect.PR != nil {
		bits = append(bits, fmt.Sprintf("#%d", r.Suspect.PR.Number))
	}
	if person != nil {
		bits = append(bits, orDefault(person.Name, person.Login))
	}
	if r.Analysis != nil {
		bits = append(bits, r.Analysis.Summary)
	} else if len(inc.Keys) > 0 {
		bits = append(bits, inc.Keys[0])
	}
	r.Short = strings.Join(bits, " · ")
	if inc.ID > 0 {
		r.Footer = fmt.Sprintf("ci-spy incident #%d", inc.ID)
	} else {
		r.Footer = "ci-spy analyze"
	}
	if r.Analysis != nil {
		r.Footer += fmt.Sprintf(" · %s · %d tokens", r.Analysis.Model, r.Analysis.InputTokens+r.Analysis.OutputTokens)
	}
	return r
}

func ResolvedReport(inc *Incident, now time.Time) Report {
	msg := fmt.Sprintf("Incident #%d (%s, first red %s) is green again after %s.",
		inc.ID, plural(len(inc.Keys), "job"), short(inc.FirstBad), Since(orDefault(inc.FirstFailedAt, inc.OpenedAt.Format(time.RFC3339)), now))
	return Report{Title: inc.Branch + " recovered", Short: msg, Note: msg}
}

// style is what differs between renderings.
type style struct {
	title   func(string) string
	heading func(string) string
	link    func(url, text string) string
	field   func(label, value string) string
	mention func(Person) string
}

var textStyle = style{
	title:   func(s string) string { return "━━ " + s + " " + strings.Repeat("━", max(3, 60-len(s))) },
	heading: func(s string) string { return s },
	link: func(url, text string) string {
		if url == "" {
			return text
		}
		return text + "  " + url
	},
	field:   func(l, v string) string { return fmt.Sprintf("%-16s%s", l+":", v) },
	mention: plainMention,
}

var markdownStyle = style{
	title:   func(s string) string { return "# " + s },
	heading: func(s string) string { return "**" + s + "**" },
	link: func(url, text string) string {
		if url == "" {
			return text
		}
		return "[" + strings.NewReplacer("[", "", "]", "").Replace(text) + "](" + url + ")"
	},
	field:   func(l, v string) string { return "**" + l + ":** " + v + "  " },
	mention: plainMention,
}

var slackStyle = style{
	title:   func(s string) string { return ":rotating_light: *" + s + "*" },
	heading: func(s string) string { return "*" + s + "*" },
	link: func(url, text string) string {
		if url == "" {
			return text
		}
		return "<" + url + "|" + strings.NewReplacer("<", "", ">", "", "|", "").Replace(text) + ">"
	},
	field: func(l, v string) string { return "*" + l + ":* " + v },
	mention: func(p Person) string {
		if p.SlackID != "" {
			return "<@" + p.SlackID + ">"
		}
		return plainMention(p)
	},
}

func plainMention(p Person) string {
	switch {
	case p.Slack != "":
		return "Slack @" + strings.TrimPrefix(p.Slack, "@")
	case p.SlackID != "":
		return "Slack <@" + p.SlackID + ">"
	default:
		return "no Slack handle known"
	}
}

func (r Report) render(s style) string {
	var b []string
	add := func(lines ...string) { b = append(b, lines...) }
	add(s.title(r.Title))
	if r.Note != "" {
		add("", r.Note)
		return strings.Join(b, "\n") + "\n"
	}
	repo := "https://github.com/" + r.Repo
	if r.Analysis != nil {
		add(s.field("Summary", r.Analysis.Summary))
	}
	add(s.field("Broken for", r.BrokenFor+" · "+plural(r.RedCommits, "red commit")))
	if r.Suspect != nil {
		pr := "(no PR)"
		if r.Suspect.PR != nil {
			pr = s.link(r.Suspect.PR.URL, fmt.Sprintf("#%d %s", r.Suspect.PR.Number, r.Suspect.PR.Title))
		}
		add(s.field("Suspect change", pr))
		add(s.field("Commit", s.link(r.Suspect.URL, short(r.Suspect.SHA)+" "+firstLine(r.Suspect.Message))))
	}
	if r.Person != nil {
		who := orDefault(r.Person.Name, r.Person.Login) + " · " + s.mention(*r.Person)
		if r.Person.Login != "" {
			who += " · GitHub @" + r.Person.Login
		}
		add(s.field("Who", who))
	}

	if a := r.Analysis; a != nil {
		add("", s.heading("Hypothesis ("+a.Confidence+" confidence)"), a.Hypothesis)
		if a.Suspect.Why != "" {
			add("", s.heading("Specific change"), a.Suspect.Why)
		}
		if len(a.Evidence) > 0 {
			add("", s.heading("Evidence"))
			for _, e := range a.Evidence {
				add(fmt.Sprintf("• %s (%s)", e.Claim, e.Source))
			}
		}
		if len(a.ValidationSteps) > 0 {
			add("", s.heading("Validate"))
			for i, v := range a.ValidationSteps {
				add(fmt.Sprintf("%d. %s", i+1, v))
			}
		}
		add("", s.heading("Proposed fix"), a.ProposedFix)
		if len(a.AlternativeExplanations) > 0 {
			add("", s.heading("Could also be"))
			for _, x := range a.AlternativeExplanations {
				add("• " + x)
			}
		}
	} else if r.AnalysisError != "" {
		add("", s.heading("No hypothesis"), r.AnalysisError)
	}

	add("", s.heading("Failing jobs"))
	for _, j := range r.Jobs {
		line := "• " + s.link(j.URL, j.Key)
		if len(j.Tests) > 0 {
			line += " — " + strings.Join(j.Tests[:min(5, len(j.Tests))], ", ")
		}
		add(line)
	}

	add("", s.heading("Blame window"))
	firstBad := s.link(repo+"/commit/"+r.FirstBad, short(r.FirstBad))
	if r.LastGood != "" {
		add(fmt.Sprintf("last green %s → first red %s", s.link(repo+"/commit/"+r.LastGood, short(r.LastGood)), firstBad))
		add(s.link(repo+"/compare/"+r.LastGood+"..."+r.FirstBad, "compare"))
	} else {
		add("first red " + firstBad + "; no green commit in the window")
	}
	if len(r.OtherBranches) > 0 {
		add("", s.field("Also failing on", strings.Join(r.OtherBranches, ", ")))
	}
	if r.CIConductor != "" {
		add("", s.field("CI Conductor", s.link(r.CIConductor, "first red commit")))
	}
	add("", "_"+r.Footer+"_")
	return strings.Join(b, "\n") + "\n"
}

func (r Report) Text() string {
	return strings.ReplaceAll(r.render(textStyle), "_"+r.Footer+"_", r.Footer)
}
func (r Report) Markdown() string { return r.render(markdownStyle) }
func (r Report) Slack() string {
	if r.Note != "" {
		return ":white_check_mark: *" + r.Title + "*\n" + r.Note
	}
	return r.render(slackStyle)
}
