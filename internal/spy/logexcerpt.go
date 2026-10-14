package spy

import (
	"regexp"
	"strings"
)

// The useful part of a GitHub Actions job log: failing test names and a short
// excerpt around the failures.

var (
	logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)
	ansi         = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	testPatterns = []*regexp.Regexp{
		// clojure.test / hawk: "FAIL in (my-test) (file.clj:12)" or "ERROR in metabase.x-test/my-test"
		regexp.MustCompile(`\b(?:FAIL|ERROR) in \(?([\w.\-/?!*<>+]+)\)?`),
		// jest: "  ● Suite › nested › test name"
		regexp.MustCompile(`^\s*● (.+›.+)$`),
		// cypress / mocha summary: "  1) Suite name"
		regexp.MustCompile(`^\s{0,4}\d+\) (\S.*)$`),
	}
	// Lines that usually sit right at the cause. ##[error] lines are kept too,
	// though they are often just "Process completed with exit code 1".
	signal = regexp.MustCompile(`(FAIL in|ERROR in|expected:|actual:|\bError:|Exception|AssertionError|Assertion failed|✖|×|●|Timed out|\bpanic\b|FAILED|##\[error\])`)
)

func CleanLog(raw string) []string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = ansi.ReplaceAllString(logTimestamp.ReplaceAllString(l, ""), "")
	}
	return lines
}

func FailingTests(lines []string, limit int) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range lines {
		for _, re := range testPatterns {
			if m := re.FindStringSubmatch(line); m != nil {
				name := strings.TrimSpace(m[1])
				if name != "" && !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Excerpt keeps context around failure lines plus the log's tail, marking gaps with "…".
func Excerpt(lines []string, maxLines, context, tail int) string {
	keep := map[int]bool{}
	budget := max(0, maxLines-tail)
	for i := 0; i < len(lines) && len(keep) < budget; i++ {
		if !signal.MatchString(lines[i]) {
			continue
		}
		for j := max(0, i-context); j <= min(len(lines)-1, i+context) && len(keep) < budget; j++ {
			keep[j] = true
		}
	}
	for j := max(0, len(lines)-tail); j < len(lines); j++ {
		keep[j] = true
	}
	var out []string
	prev := -2
	for i := range lines {
		if !keep[i] {
			continue
		}
		if i != prev+1 {
			out = append(out, "…")
		}
		out = append(out, lines[i])
		prev = i
	}
	return strings.Join(out, "\n")
}
