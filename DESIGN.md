# ci-spy design

Grounded in `README.md`. Defaults picked where the README leaves a choice open are marked **(default)**.

> Status: implemented in Go (`watch` and `analyze <commit>`). The CI Conductor adapter (build step 5) is not: its MCP server needs an OAuth login and its API only accepts an API key on `/api/flake-probability`, so GitHub is the data source for now.

## What it does

Every ~20 minutes during working hours it checks `master` and `release-x.*` branches of metabase/metabase. When a branch goes from green to red, it opens an **incident**, does one expensive analysis, and pings you with a hypothesis you can validate in a couple of minutes. Later red commits that fail the same way are attached to the same incident silently. When the branch goes green again, you get a short "resolved" ping.

## Pipeline

```
schedule ─▶ collect (cheap) ─▶ classify/dedup (cheap) ─▶ analyze (LLM, rare) ─▶ notify
                         └──────────── state store (SQLite) ───────────┘
```

### 1. Schedule
- `ci-spy watch` loops every `schedule.interval` (20 min). `--once` does a single check for cron/launchd. `ci-spy analyze <commit>` runs the same evidence/analysis/report on demand for one commit.
- Quiet hours live in config, enforced in code, so the same CLI works later from GitHub Actions cron or a webhook:
  `active: { days: Mon-Fri, hours: "08:30-18:00", tz: America/... }`.
  Outside the window the tick does nothing (no GitHub or AI usage). The first tick inside the window catches up, and "broken for" is computed from job completion times, so it stays accurate.
- Webhook mode later: a `serve` command accepting `workflow_run` / `check_suite` events and calls the same `tick(branch)`.

### 2. Collect (no LLM)
Behind a `CiSource` interface with two adapters:
- **GitHub adapter (default)**: uses `GITHUB_TOKEN` or the `gh` token. Per watched branch: last N commits, workflow runs per commit (cached once settled), and job lists only for workflows that failed in the window.
- **CI Conductor adapter**: same interface backed by ci-conductor's queries (`get_ci_health`, `get_branch_commits`, `get_failing_tests`, `get_job_failures`, `get_test_flake_history`). This is the seam for folding ci-spy into CI Conductor later.

Output per commit: `{sha, pr, author, time, jobs: [{name, conclusion, failingTests[], errorExcerpt}]}`.

### 3. Classify and dedup (no LLM)
- **Job key** = workflow + job name with matrix shard numbers stripped, so `be-tests (1/2)` and `be-tests (2/2)` are the same job.
- **Is it real?** A job counts as broken only if it is red at head on 2 consecutive commits (pending commits skipped), or failed its re-run. Jobs that flipped pass/fail 4+ times elsewhere in the window need one more red commit.
- **Is it new?** Jobs that started failing on the same commit are one breakage. If it shares a first-bad commit or any job with an open incident on that branch, it attaches. Otherwise it opens a new incident.
- **Blame window** = last green commit ..first red commit for that signature. Usually one commit.
- **Resolve** when none of the incident's jobs is red at head any more.

### 4. Analyze (LLM, once per incident)
Builds an evidence bundle and asks Claude for structured JSON:
- Evidence: commits/PRs in the blame window (title, author, files touched, diff trimmed to relevant files), failing job names, failing tests, error log excerpts, flake history for those tests, whether the same tests fail on other branches.
- Output schema: `hypothesis`, `suspect {sha, pr, author}`, `confidence`, `evidence[]` (each pointing to a link or log line), `validate[]` (exact commands or links, e.g. "rerun job X", "run `clojure -X:dev:test :only ...` on sha~1 vs sha"), `proposedFix`, `tag`.
- **(default)** Sonnet for the analysis, with a per-day budget cap (e.g. 10 analyses) in config. Uses your Anthropic API key, not a local harness.

### 5. Notify
- **(default)** macOS notification (short) plus a Slack message (full report) via an incoming webhook to a DM/channel. Pluggable `Notifier` interface.
- People map: `people.yaml` maps GitHub login to name and Slack handle; fallback is Slack `users.lookupByEmail` on the commit email if a bot token is configured.

Report contents (from the README):
- what broke: branch, jobs, tests, links to the failing runs
- time since first failure, commits since (how many are red)
- suspect change: commit + PR link + the specific hunk
- who: name + `@slack-handle`
- hypothesis, confidence, how to validate, proposed fix

### 6. State (`data/state.json`)
- incidents (branch, job keys, first bad / last good commit, red commits, analysis, when notified / resolved)
- a cache of settled commits' runs and jobs (pruned after 7 days)
- analyses used per day

## Stack
Go, one dependency (BurntSushi/toml). GitHub and Anthropic are called over plain HTTP. State is a JSON file (`data/state.json`). Config is `ci-spy.toml`.

## Test case
`internal/spy/spy_test.go` models master around `ff22433a` (#82773), where cross-version migration tests started failing: it must open exactly one incident, blame #82773, attach the following red commits without re-analyzing, and send one recovery ping. Against the real thing: `ci-spy analyze ff22433a`.

## Build order
1. Config, state store, GitHub collector, `ci-spy status` (prints branch health). No notifications.
2. Classify/dedup + incidents, verified with the #82773-shaped test.
3. Analyzer + report rendering (`--dry-run` prints the report).
4. Notifiers + launchd plist + quiet hours.
5. CI Conductor adapter.
