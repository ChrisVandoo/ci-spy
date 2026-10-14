package spy

import "time"

// Shapes shared by the collector, the classifier and the report. Everything the
// classifier reads is plain data (no clients, no clocks) so detection can be
// tested without a network.

// Conclusions that mean "this job is broken" (cancelled and skipped do not).
var failed = map[string]bool{"failure": true, "timed_out": true, "startup_failure": true}

type JobRun struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	RunAttempt int    `json:"run_attempt"`
	Workflow   string `json:"workflow"`
	Name       string `json:"name"`
	// Stable identity across commits: workflow + job name with matrix shard
	// numbers stripped (see JobKey).
	Key         string `json:"key"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	URL         string `json:"url"`
}

type WorkflowRun struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	RunAttempt int    `json:"run_attempt"`
	URL        string `json:"url"`
	// False when the workflow never failed in the window, so its jobs were not
	// fetched and are inferred from the run conclusion.
	JobsFetched bool `json:"jobs_fetched"`
}

type CommitInfo struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	AuthorName  string `json:"author_name"`
	AuthorEmail string `json:"author_email"`
	AuthorLogin string `json:"author_login"`
	CommittedAt string `json:"committed_at"`
	URL         string `json:"url"`
}

type CommitSnapshot struct {
	CommitInfo
	Workflows []WorkflowRun
	Jobs      []JobRun
}

type BranchSnapshot struct {
	Repo    string
	Branch  string
	TakenAt time.Time
	// Newest first, in branch order.
	Commits []CommitSnapshot
}

type Breakage struct {
	Key string
	// Oldest commit of the current failing streak.
	FirstBad string
	// Newest commit before the streak where the job passed; "" when the streak
	// runs past the end of the window.
	LastGood string
	// Failing commits in the streak, newest first.
	FailingSHAs      []string
	FirstFailedAt    string
	Confirmed        bool
	ConfirmedByRerun bool
	// Pass/fail flips for this job in the window outside the current streak.
	Flips int
}

type Incident struct {
	ID                 int        `json:"id"`
	Repo               string     `json:"repo"`
	Branch             string     `json:"branch"`
	Keys               []string   `json:"keys"`
	FirstBad           string     `json:"first_bad"`
	LastGood           string     `json:"last_good"`
	FailingSHAs        []string   `json:"failing_shas"`
	FirstFailedAt      string     `json:"first_failed_at"`
	OpenedAt           time.Time  `json:"opened_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
	Analysis           *Analysis  `json:"analysis,omitempty"`
	AnalysisError      string     `json:"analysis_error,omitempty"`
	NotifiedAt         *time.Time `json:"notified_at,omitempty"`
	ResolvedNotifiedAt *time.Time `json:"resolved_notified_at,omitempty"`
}

type Analysis struct {
	Summary                 string          `json:"summary"`
	Hypothesis              string          `json:"hypothesis"`
	Confidence              string          `json:"confidence"`
	Suspect                 AnalysisSuspect `json:"suspect"`
	Evidence                []EvidenceItem  `json:"evidence"`
	ValidationSteps         []string        `json:"validation_steps"`
	ProposedFix             string          `json:"proposed_fix"`
	AlternativeExplanations []string        `json:"alternative_explanations"`
	Model                   string          `json:"model"`
	InputTokens             int             `json:"input_tokens"`
	OutputTokens            int             `json:"output_tokens"`
}

type AnalysisSuspect struct {
	SHA      string `json:"sha"`
	PRNumber int    `json:"pr_number"`
	Why      string `json:"why"`
}

type EvidenceItem struct {
	Claim  string `json:"claim"`
	Source string `json:"source"`
}

type Person struct {
	Name    string `toml:"name" json:"name,omitempty"`
	Slack   string `toml:"slack" json:"slack,omitempty"`
	SlackID string `toml:"slackId" json:"slack_id,omitempty"`
	Login   string `toml:"-" json:"login,omitempty"`
	Email   string `toml:"-" json:"email,omitempty"`
}

type PR struct {
	Number int
	Title  string
	URL    string
	Body   string
	Author string
}

type ChangedFile struct {
	Filename  string
	Status    string
	Additions int
	Deletions int
	Patch     string // empty when it didn't fit the budget
}

type SuspectCommit struct {
	CommitInfo
	PR           *PR
	Files        []ChangedFile
	OmittedFiles int
}

type FailingJobEvidence struct {
	Key          string
	URL          string
	Conclusion   string
	FailingTests []string
	Excerpt      string
}

type BranchKeys struct {
	Branch string
	Keys   []string
}

type Evidence struct {
	Repo              string
	Branch            string
	Keys              []string
	FirstBad          CommitInfo
	LastGood          *CommitInfo
	RedCommits        int
	FirstFailedAt     string
	Suspects          []SuspectCommit
	SuspectsTruncated bool
	Jobs              []FailingJobEvidence
	OtherBranches     []BranchKeys
}
