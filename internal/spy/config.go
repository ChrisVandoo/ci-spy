package spy

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Configuration: ci-spy.toml (optional) over the defaults below. Secrets come
// from the environment, never the file: ANTHROPIC_API_KEY, SLACK_WEBHOOK_URL,
// GITHUB_TOKEN (falls back to `gh auth token`) and SLACK_BOT_TOKEN (optional,
// for looking up Slack users by commit email).

type Config struct {
	Repo string `toml:"repo"`
	// Exact names or simple globs ("release-x.*").
	Branches []string `toml:"branches"`
	// Only the newest N release branches matching a glob are watched.
	ReleaseBranchLimit int `toml:"releaseBranchLimit"`
	// How many commits back from each branch head to look at.
	CommitWindow int `toml:"commitWindow"`
	// Workflow run events that count. Scheduled/nightly runs are noisy by design.
	Events    []string          `toml:"events"`
	Schedule  ScheduleConfig    `toml:"schedule"`
	Detection DetectionConfig   `toml:"detection"`
	Analysis  AnalysisConfig    `toml:"analysis"`
	Notify    NotifyConfig      `toml:"notify"`
	DataDir   string            `toml:"dataDir"`
	People    map[string]Person `toml:"people"`
}

type ScheduleConfig struct {
	// How often `watch` checks.
	Interval string   `toml:"interval"`
	Timezone string   `toml:"timezone"`
	Days     []string `toml:"days"`
	Start    string   `toml:"start"`
	End      string   `toml:"end"`
}

type DetectionConfig struct {
	// A job must fail on this many consecutive commits (or fail again on a
	// re-run) before it counts as broken rather than flaky.
	MinConsecutiveFailures int `toml:"minConsecutiveFailures"`
	// Jobs that flip pass/fail this often elsewhere in the window need one more
	// consecutive failure.
	FlakyFlips int `toml:"flakyFlips"`
	// Regexes matched against job keys; matching jobs are never reported.
	IgnoreJobs []string `toml:"ignoreJobs"`
}

type AnalysisConfig struct {
	Enabled           bool   `toml:"enabled"`
	Model             string `toml:"model"`
	MaxPerDay         int    `toml:"maxPerDay"`
	MaxSuspectCommits int    `toml:"maxSuspectCommits"`
	DiffCharBudget    int    `toml:"diffCharBudget"`
	LogExcerptLines   int    `toml:"logExcerptLines"`
	MaxJobLogs        int    `toml:"maxJobLogs"`
}

type NotifyConfig struct {
	MacOS      bool   `toml:"macos"`
	Slack      bool   `toml:"slack"`
	ReportsDir string `toml:"reportsDir"`
	// Adds a link to CI Conductor's page for the first red commit.
	CIConductorURL string `toml:"ciConductorUrl"`
}

func DefaultConfig() Config {
	return Config{
		Repo:               "metabase/metabase",
		Branches:           []string{"master", "release-x.*"},
		ReleaseBranchLimit: 3,
		CommitWindow:       25,
		Events:             []string{"push"},
		Schedule: ScheduleConfig{
			Interval: "20m",
			Timezone: "Local",
			Days:     []string{"mon", "tue", "wed", "thu", "fri"},
			Start:    "09:00",
			End:      "18:00",
		},
		Detection: DetectionConfig{MinConsecutiveFailures: 2, FlakyFlips: 4},
		Analysis: AnalysisConfig{
			Enabled:           true,
			Model:             "claude-sonnet-5-5",
			MaxPerDay:         10,
			MaxSuspectCommits: 8,
			DiffCharBudget:    60_000,
			LogExcerptLines:   120,
			MaxJobLogs:        3,
		},
		Notify:  NotifyConfig{MacOS: true, Slack: true, ReportsDir: "reports"},
		DataDir: "data",
		People:  map[string]Person{},
	}
}

// LoadConfig decodes path (if it exists) over the defaults, then merges
// people.toml next to it.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if _, err := toml.DecodeFile(path, &cfg); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	people := map[string]Person{}
	if _, err := toml.DecodeFile("people.toml", &people); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if cfg.People == nil {
		cfg.People = map[string]Person{}
	}
	for login, p := range people {
		if _, ok := cfg.People[login]; !ok {
			cfg.People[login] = p
		}
	}
	return cfg, nil
}

func (s ScheduleConfig) location() *time.Location {
	if loc, err := time.LoadLocation(s.Timezone); err == nil {
		return loc
	}
	return time.Local
}

func (s ScheduleConfig) IntervalDuration() time.Duration {
	if d, err := time.ParseDuration(s.Interval); err == nil && d > 0 {
		return d
	}
	return 20 * time.Minute
}

func clockMinutes(hhmm string) int {
	h, m, _ := strings.Cut(hhmm, ":")
	hi, _ := strconv.Atoi(h)
	mi, _ := strconv.Atoi(m)
	return hi*60 + mi
}

// InActiveWindow reports whether now falls inside the configured working days
// and hours, in the configured timezone.
func (s ScheduleConfig) InActiveWindow(now time.Time) bool {
	local := now.In(s.location())
	day := strings.ToLower(local.Weekday().String()[:3])
	ok := false
	for _, d := range s.Days {
		if len(d) >= 3 && strings.ToLower(d[:3]) == day {
			ok = true
		}
	}
	if !ok {
		return false
	}
	t := local.Hour()*60 + local.Minute()
	return t >= clockMinutes(s.Start) && t < clockMinutes(s.End)
}

func (s ScheduleConfig) LocalDay(now time.Time) string {
	return now.In(s.location()).Format("2006-01-02")
}
