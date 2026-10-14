package spy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Who to tag: GitHub login -> name and Slack handle, from people.toml first and
// Slack's users.lookupByEmail second (needs SLACK_BOT_TOKEN with users:read.email).

func ResolvePerson(login, name, email string, people map[string]Person, st *State) Person {
	p := Person{Name: name, Login: login, Email: email}
	if c, ok := people[login]; ok && login != "" {
		p.Name = orDefault(c.Name, name)
		p.Slack, p.SlackID = c.Slack, c.SlackID
	}
	token := os.Getenv("SLACK_BOT_TOKEN")
	if p.Slack != "" || p.SlackID != "" || token == "" || email == "" || strings.HasSuffix(email, "noreply.github.com") {
		return p
	}
	key := "slack-user:" + email
	var found *Person
	if !st.CacheGet(key, &found) {
		found = lookupSlack(token, email)
		st.CacheSet(key, found)
	}
	if found != nil {
		p.Name = orDefault(found.Name, p.Name)
		p.Slack, p.SlackID = found.Slack, found.SlackID
	}
	return p
}

func lookupSlack(token, email string) *Person {
	req, _ := http.NewRequest("GET", "https://slack.com/api/users.lookupByEmail?email="+url.QueryEscape(email), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil // Slack being down must not block the report.
	}
	defer res.Body.Close()
	var body struct {
		OK   bool `json:"ok"`
		User struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if json.NewDecoder(res.Body).Decode(&body) != nil || !body.OK {
		return nil
	}
	return &Person{SlackID: body.User.ID, Slack: orDefault(body.User.Profile.DisplayName, body.User.Name), Name: body.User.RealName}
}
