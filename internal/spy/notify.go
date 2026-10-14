package spy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Delivery for `watch`. The report is always printed and saved; the macOS
// notification and the Slack webhook are best-effort extras.

func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Notify prints the report, saves it as reports/<name>.md and, unless dryRun,
// sends it to the desktop and Slack. Failures are returned, not fatal.
func Notify(r Report, cfg NotifyConfig, out io.Writer, name string, dryRun bool) []error {
	var errs []error
	fmt.Fprintln(out, r.Text())
	if name != "" {
		if err := os.MkdirAll(cfg.ReportsDir, 0o755); err != nil {
			errs = append(errs, err)
		} else if err := os.WriteFile(filepath.Join(cfg.ReportsDir, name+".md"), []byte(r.Markdown()), 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	if dryRun {
		return errs
	}
	if cfg.MacOS && runtime.GOOS == "darwin" {
		short := r.Short
		if len(short) > 200 {
			short = short[:200]
		}
		script := fmt.Sprintf("display notification %s with title %s", appleScriptString(short), appleScriptString("ci-spy: "+r.Title))
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			errs = append(errs, fmt.Errorf("macOS notification: %w", err))
		}
	}
	if hook := os.Getenv("SLACK_WEBHOOK_URL"); cfg.Slack && hook != "" {
		body, _ := json.Marshal(map[string]string{"text": r.Slack()})
		res, err := (&http.Client{Timeout: 15 * time.Second}).Post(hook, "application/json", bytes.NewReader(body))
		if err != nil {
			errs = append(errs, fmt.Errorf("slack: %w", err))
		} else {
			if res.StatusCode >= 300 {
				msg, _ := io.ReadAll(res.Body)
				errs = append(errs, fmt.Errorf("slack %d: %s", res.StatusCode, msg))
			}
			res.Body.Close()
		}
	}
	return errs
}
