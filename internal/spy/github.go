package spy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitHub is a minimal REST client. BaseURL is swappable so tests can point it
// at a fake server. Requests counts calls so a pass can report its cost.
type GitHub struct {
	BaseURL  string
	Token    string
	HTTP     *http.Client
	Requests int
}

func NewGitHub(token string) *GitHub {
	return &GitHub{BaseURL: "https://api.github.com", Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// ResolveToken prefers GITHUB_TOKEN, then the gh CLI's stored token.
func ResolveToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (g *GitHub) get(path, accept string) ([]byte, error) {
	g.Requests++
	req, err := http.NewRequest("GET", g.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ci-spy")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	res, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("GitHub %d for %s: %s", res.StatusCode, path, snippet)
	}
	return body, nil
}

func (g *GitHub) JSON(path string, out any) error {
	body, err := g.get(path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// Text fetches raw text, e.g. job logs (GitHub redirects to blob storage).
func (g *GitHub) Text(path string) (string, error) {
	body, err := g.get(path, "text/plain")
	return string(body), err
}

// Paginate follows page= until a short page. key names the array inside an
// object response ("" when the response is the array itself).
func Paginate[T any](g *GitHub, path, key string, maxPages int) ([]T, error) {
	var out []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= maxPages; page++ {
		body, err := g.get(fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), "application/vnd.github+json")
		if err != nil {
			return out, err
		}
		var items []T
		if key == "" {
			err = json.Unmarshal(body, &items)
		} else {
			var wrapper map[string]json.RawMessage
			if err = json.Unmarshal(body, &wrapper); err == nil {
				err = json.Unmarshal(wrapper[key], &items)
			}
		}
		if err != nil {
			return out, err
		}
		out = append(out, items...)
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}
