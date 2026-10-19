// ci-spy watches master and release branches for CI breakages and explains them.
//
//	ci-spy watch [--once] [--force] [--dry-run] [--no-ai]
//	ci-spy analyze <sha|commit-url> [--branch master] [--no-ai]
//
// Both print the same report. watch also saves it under reports/ and sends it
// to Slack and the macOS Notification Center.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ci-spy/internal/spy"
)

const usage = `usage:
  ci-spy watch [flags]                 check watched branches every interval and report new breakages
  ci-spy analyze <sha|commit-url> [flags]   explain why a commit is red, right now

watch flags:
  --once      run a single check and exit (for cron/launchd)
  --force     check even outside the configured working hours
  --dry-run   print reports but don't send Slack/desktop notifications or mark them sent
  --no-ai     skip the Claude analysis

analyze flags:
  --branch    branch the commit is on, for the report title (default "master")
  --no-ai     skip the Claude analysis

common:
  --config    config file (default "ci-spy.toml")
  --log-level minimum log level: debug, info, warn, error (default "info")

environment: ANTHROPIC_API_KEY, SLACK_WEBHOOK_URL, GITHUB_TOKEN (else ` + "`gh auth token`" + `), SLACK_BOT_TOKEN
`

// parse lets flags appear before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		fmt.Print(usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	configPath := fs.String("config", "ci-spy.toml", "")
	logLevel := fs.String("log-level", "info", "")
	noAI := fs.Bool("no-ai", false, "")
	once := fs.Bool("once", false, "")
	force := fs.Bool("force", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	branch := fs.String("branch", "master", "")

	switch cmd {
	case "watch", "analyze":
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	pos := parse(fs, args)

	logger, err := newLogger(os.Stderr, *logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	logger = logger.With("command", cmd)
	logger.Info("starting ci-spy")
	cfg, err := spy.LoadConfig(*configPath)
	if err != nil {
		logger.Error("loading config failed", "error", err)
		return 1
	}
	logger.Debug("config loaded", "path", *configPath, "repo", cfg.Repo, "branches", cfg.Branches)
	token := spy.ResolveToken()
	if token == "" {
		logger.Warn("no GitHub token (set GITHUB_TOKEN or run `gh auth login`); using anonymous rate limits")
	}
	state, err := spy.LoadState(cfg.DataDir + "/state.json")
	if err != nil {
		logger.Error("loading state failed", "error", err)
		return 1
	}
	d := spy.Deps{GH: spy.NewGitHub(token), State: state, Config: cfg, Out: os.Stdout, Log: logger, DryRun: *dryRun}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" && !*noAI {
		d.Analyzer = spy.NewAnalyzer(key, cfg.Analysis.Model)
	}

	switch cmd {
	case "watch":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := spy.Watch(ctx, d, *once, *force); err != nil {
			logger.Error("command failed", "error", err)
			return 1
		}
	case "analyze":
		if len(pos) != 1 {
			fmt.Fprint(os.Stderr, usage)
			return 2
		}
		reports, err := spy.AnalyzeCommit(d, pos[0], *branch, time.Now())
		if err != nil {
			logger.Error("command failed", "error", err)
			return 1
		}
		for _, r := range reports {
			fmt.Println(r.Text())
		}
	}
	logger.Info("command completed")
	return 0
}
