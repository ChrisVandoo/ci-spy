## ci-spy

A tool that watches CI and reports when and (probably) why it is exploding.

### POC

Project Goal: notify me when master or a release branch starts exploding and give me a hypothesis as to why this is happening that includes all the information I need to validate the hypothesis and/or tag the person responsible.

- ideally would trigger on webhook, could initially be configured to poll Github hourly? maybe every 20 or 30 minutes
    - CI Conductor is very helpful for quickly getting the state of CI, should consider if it is more effective to query this instead
- I should be notified, could this just be a slack ping? or configure helpful terminal notifications?
    - this should be a configurable schedule—I don’t want notifications after hours or on weekends, I also don’t want to abuse the Github API (or consume all my AI usage)
- needs to store failures to deduplicate when one failures causes a train of failing commits
    - do quick & cheap analysis to see if this is the same issue before triggering the more expensive bit
- I want a proposed analysis, root cause, and potential fix
    - what change caused the failure
    - who made the change, (name & slack handle)
    - time since initial failure
    - specific change that likely caused the failure
    - proposed solution

Also want this to be something that could be integrated into CI Conductor eventually, shouldn’t be tied to my local LLM harness. Might be a good place to use my Anthropic credits though.

---

## Usage

Requires Go 1.24+ and either `gh auth login` or `GITHUB_TOKEN`.

```sh
go build -o ci-spy .
cp ci-spy.example.toml ci-spy.toml      # optional; defaults watch master + the newest 3 release-x.* branches
cp people.example.toml people.toml      # optional; GitHub login -> Slack handle
export ANTHROPIC_API_KEY=…              # for the hypothesis
export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/…   # optional

./ci-spy analyze ff22433a               # why is this commit red? (sha or GitHub commit URL)
./ci-spy watch --dry-run --force --once # one check right now, print instead of notify
./ci-spy watch                          # check every 20 min during working hours, notify on new breakages
```

Both commands print the same report. `watch` also saves it to `reports/incident-<id>.md` and sends it to Slack and the macOS Notification Center, then a short "recovered" message when the branch goes green.

| | `watch` | `analyze <commit>` |
|---|---|---|
| What it looks at | heads of the watched branches | the commit you name, as if it were the head |
| Counts as broken | red on 2 consecutive commits, or red after a re-run | any red job |
| Remembers incidents | yes, in `data/state.json`, so a red train is one report | no |
| Sends notifications | yes (unless `--dry-run`) | no, prints only |
| Daily analysis cap | yes (`analysis.maxPerDay`) | no |

Flags: `--once` (single check, for cron/launchd), `--force` (ignore working hours), `--dry-run`, `--no-ai`, `--branch` (analyze: branch name for the title), `--config`.

To keep `watch` running in the background on a Mac, see [launchd/com.ci-spy.watch.plist](launchd/com.ci-spy.watch.plist).

## How it works

See [DESIGN.md](DESIGN.md). In short (all in `internal/spy`):

1. **Collect** (`collect.go`): branch commits, then workflow runs per commit, then jobs, but only for workflows that failed somewhere in the window. Settled commits are cached, so a green branch costs about 2 requests per check.
2. **Detect and dedup** (`classify.go`, no AI): jobs that started failing on the same commit form one incident. Later red commits attach to the open incident. Incidents resolve when none of their jobs is red at head.
3. **Evidence** (`evidence.go`): commits between last green and first red, their PRs, diffs ranked by overlap with the failing jobs and tests, and log excerpts with failing test names.
4. **Analyze** (`analyze.go`): one Claude call per incident with a forced tool call.
5. **Report** (`report.go`): one report rendered three ways (terminal, Markdown, Slack).

`go test ./...` runs everything against fake GitHub and Anthropic servers, including a breakage shaped like #82773.
