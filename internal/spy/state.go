package spy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is everything ci-spy remembers between passes: incidents, a response
// cache for settled commits, and the daily analysis budget. It is small, so it
// lives in one JSON file written atomically after each pass.
type State struct {
	path      string
	NextID    int                   `json:"next_id"`
	Incidents []*Incident           `json:"incidents"`
	Cache     map[string]cacheEntry `json:"cache"`
	Budget    map[string]int        `json:"budget"`
}

type cacheEntry struct {
	Value    json.RawMessage `json:"value"`
	StoredAt time.Time       `json:"stored_at"`
}

// LoadState reads path; an empty path gives in-memory state that Save ignores.
func LoadState(path string) (*State, error) {
	s := &State{path: path, NextID: 1, Cache: map[string]cacheEntry{}, Budget: map[string]int{}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	s.path = path
	if s.Cache == nil {
		s.Cache = map[string]cacheEntry{}
	}
	if s.Budget == nil {
		s.Budget = map[string]int{}
	}
	return s, nil
}

func (s *State) Save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) CacheGet(key string, out any) bool {
	e, ok := s.Cache[key]
	return ok && json.Unmarshal(e.Value, out) == nil
}

func (s *State) CacheSet(key string, v any) {
	if data, err := json.Marshal(v); err == nil {
		s.Cache[key] = cacheEntry{Value: data, StoredAt: time.Now()}
	}
}

// PruneCache drops entries for commits that have long since left every window,
// and resolved incidents nobody will look at again.
func (s *State) PruneCache(now time.Time) {
	for k, e := range s.Cache {
		if now.Sub(e.StoredAt) > 7*24*time.Hour {
			delete(s.Cache, k)
		}
	}
	kept := s.Incidents[:0]
	for _, i := range s.Incidents {
		if i.ResolvedAt == nil || now.Sub(*i.ResolvedAt) < 30*24*time.Hour {
			kept = append(kept, i)
		}
	}
	s.Incidents = kept
}

func (s *State) OpenIncidents(repo, branch string) []*Incident {
	var out []*Incident
	for _, i := range s.Incidents {
		if i.ResolvedAt == nil && i.Repo == repo && (branch == "" || i.Branch == branch) {
			out = append(out, i)
		}
	}
	return out
}

func (s *State) Incident(id int) *Incident {
	for _, i := range s.Incidents {
		if i.ID == id {
			return i
		}
	}
	return nil
}

// PendingNotifications: new incidents, and resolutions of incidents you were
// told about. An incident that resolved before anyone was told stays silent.
func (s *State) PendingNotifications(repo string) []*Incident {
	var out []*Incident
	for _, i := range s.Incidents {
		if i.Repo != repo {
			continue
		}
		if (i.NotifiedAt == nil && i.ResolvedAt == nil) ||
			(i.NotifiedAt != nil && i.ResolvedAt != nil && i.ResolvedNotifiedAt == nil) {
			out = append(out, i)
		}
	}
	return out
}

func (s *State) Open(repo, branch string, g BreakageGroup, now time.Time) *Incident {
	i := &Incident{
		ID:            s.NextID,
		Repo:          repo,
		Branch:        branch,
		Keys:          g.Keys,
		FirstBad:      g.FirstBad,
		LastGood:      g.LastGood,
		FailingSHAs:   g.FailingSHAs,
		FirstFailedAt: g.FirstFailedAt,
		OpenedAt:      now,
		UpdatedAt:     now,
	}
	s.NextID++
	s.Incidents = append(s.Incidents, i)
	return i
}
