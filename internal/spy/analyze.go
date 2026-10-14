package spy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The expensive step: one Claude call per incident, forced through a tool so
// the answer comes back as structured JSON (the same approach as CI Conductor's
// log analysis in server/src/anthropic.ts).

const (
	anthropicVersion = "2023-06-01"
	maxOutputTokens  = 3000
)

// Analyzer calls the Messages API. URL is swappable for tests.
type Analyzer struct {
	URL    string
	APIKey string
	Model  string
	HTTP   *http.Client
}

func NewAnalyzer(apiKey, model string) *Analyzer {
	return &Analyzer{URL: "https://api.anthropic.com/v1/messages", APIKey: apiKey, Model: model, HTTP: &http.Client{Timeout: 3 * time.Minute}}
}

const systemPrompt = `You triage CI breakages on protected branches (master and release branches) of a large monorepo.
You get the jobs that started failing, the commits in the blame window (last green commit to first red commit) with their PRs and diffs, and excerpts of the failing job logs.

Work out the most likely cause and report it through the tool. Rules:
- Ground every claim in the evidence given: cite a file and hunk, a log line, or a link. Never invent test names, files or people.
- The reader will decide whether to ping the suspect author based on your report, so be honest about uncertainty. If the evidence points to infrastructure, a flaky test, or an external dependency rather than a change, say so and leave suspect.sha null.
- Validation steps must be concrete and quick: exact commands (with shas), specific test names to run, or specific links to open. Prefer steps that distinguish your hypothesis from the alternatives.
- The proposed fix should be specific (what to change or revert, and where).`

const toolName = "report_breakage_analysis"

var toolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {"type": "string", "description": "One line, under 120 characters."},
    "hypothesis": {"type": "string", "description": "2-5 sentences: what broke, which change caused it, and the mechanism."},
    "confidence": {"type": "string", "enum": ["high", "medium", "low"]},
    "suspect": {
      "type": "object",
      "properties": {
        "sha": {"type": ["string", "null"], "description": "Full sha of the suspect commit, or null."},
        "pr_number": {"type": ["integer", "null"]},
        "why": {"type": "string", "description": "The specific hunk or change and why it breaks."}
      },
      "required": ["sha", "pr_number", "why"]
    },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "claim": {"type": "string"},
          "source": {"type": "string", "description": "URL, file:line, or 'log: <job>'."}
        },
        "required": ["claim", "source"]
      }
    },
    "validation_steps": {"type": "array", "items": {"type": "string"}},
    "proposed_fix": {"type": "string"},
    "alternative_explanations": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["summary", "hypothesis", "confidence", "suspect", "evidence", "validation_steps", "proposed_fix", "alternative_explanations"]
}`)

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// RenderEvidence is the user message the model sees.
func RenderEvidence(e *Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nBranch: %s\n\n", e.Repo, e.Branch)
	fmt.Fprintf(&b, "Failing jobs (%d):\n", len(e.Keys))
	for _, k := range e.Keys {
		fmt.Fprintf(&b, "- %s\n", k)
	}
	fmt.Fprintf(&b, "\nFirst red commit: %s (%s)\n", e.FirstBad.SHA, e.FirstBad.CommittedAt)
	if e.LastGood != nil {
		fmt.Fprintf(&b, "Last green commit: %s (%s)\n", e.LastGood.SHA, e.LastGood.CommittedAt)
	} else {
		b.WriteString("Last green commit: not in the window (broken for longer than the window covers)\n")
	}
	fmt.Fprintf(&b, "Red commits since: %d\nFirst failure finished at: %s\n", e.RedCommits, orDefault(e.FirstFailedAt, "unknown"))
	if len(e.OtherBranches) > 0 {
		var names []string
		for _, o := range e.OtherBranches {
			names = append(names, o.Branch)
		}
		fmt.Fprintf(&b, "The same jobs are also failing on: %s\n", strings.Join(names, ", "))
	}

	trunc := ""
	if e.SuspectsTruncated {
		trunc = ", truncated"
	}
	fmt.Fprintf(&b, "\nCommits in the blame window (%d%s):\n", len(e.Suspects), trunc)
	for _, s := range e.Suspects {
		login := ""
		if s.AuthorLogin != "" {
			login = " (@" + s.AuthorLogin + ")"
		}
		fmt.Fprintf(&b, "\n=== commit %s by %s <%s>%s at %s\nMessage: %s\n", s.SHA, s.AuthorName, s.AuthorEmail, login, s.CommittedAt, firstLine(s.Message))
		if s.PR != nil {
			fmt.Fprintf(&b, "PR #%d: %s (%s) by @%s\nPR description:\n%s\n", s.PR.Number, s.PR.Title, s.PR.URL, orDefault(s.PR.Author, "unknown"), orDefault(s.PR.Body, "(empty)"))
		}
		b.WriteString("Files:\n")
		for _, f := range s.Files {
			omitted := ""
			if f.Patch == "" {
				omitted = " [diff omitted]"
			}
			fmt.Fprintf(&b, "- %s %s (+%d -%d)%s\n", f.Status, f.Filename, f.Additions, f.Deletions, omitted)
		}
		if s.OmittedFiles > 0 {
			fmt.Fprintf(&b, "- …and %d more files\n", s.OmittedFiles)
		}
		for _, f := range s.Files {
			if f.Patch != "" {
				fmt.Fprintf(&b, "--- diff %s\n%s\n", f.Filename, f.Patch)
			}
		}
	}
	for _, j := range e.Jobs {
		fmt.Fprintf(&b, "\n=== log excerpt: %s (%s) %s\n", j.Key, j.Conclusion, j.URL)
		if len(j.FailingTests) > 0 {
			fmt.Fprintf(&b, "Failing tests: %s\n", strings.Join(j.FailingTests, ", "))
		}
		b.WriteString(j.Excerpt + "\n")
	}
	return b.String()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (a *Analyzer) Analyze(e *Evidence) (*Analysis, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":       a.Model,
		"max_tokens":  maxOutputTokens,
		"system":      systemPrompt,
		"tools":       []any{map[string]any{"name": toolName, "description": "Report the most likely cause of the CI breakage and how to validate and fix it.", "input_schema": toolSchema}},
		"tool_choice": map[string]any{"type": "tool", "name": toolName},
		"messages":    []any{map[string]any{"role": "user", "content": RenderEvidence(e)}},
	})
	req, err := http.NewRequest("POST", a.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("Anthropic %d: %.300s", res.StatusCode, body)
	}
	var parsed struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	for _, c := range parsed.Content {
		if c.Type != "tool_use" || c.Name != toolName {
			continue
		}
		var out Analysis
		if err := json.Unmarshal(c.Input, &out); err != nil {
			return nil, err
		}
		out.Model = a.Model
		out.InputTokens = parsed.Usage.InputTokens
		out.OutputTokens = parsed.Usage.OutputTokens
		return &out, nil
	}
	return nil, errors.New("Anthropic response had no analysis tool call")
}
