package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cacheMu keeps two tabs finishing at once from losing each other's save.
var cacheMu sync.Mutex

// lists.json keeps the last lists the picker loaded, so the next popup can
// show them at once while GitHub is asked again (a page of PRs takes GitHub
// a couple of seconds). What's shown is replaced as soon as the fresh answer
// arrives. It holds titles, branch names and states: readable only by you,
// like the rest of the state dir.
type listCache struct {
	Saved time.Time             `json:"saved"`
	Repo  string                `json:"repo"` // the repo tab's repo, by key
	Tabs  map[tab][]pullRequest `json:"tabs"`
}

func listCachePath() string { return filepath.Join(stateDir(), "lists.json") }

// readListCache returns the cached lists; the repo tab's only when it was for
// the same repo.
func readListCache(repo *repoRef) map[tab][]pullRequest {
	var c listCache
	data, err := os.ReadFile(listCachePath())
	if err != nil || json.Unmarshal(data, &c) != nil || c.Tabs == nil {
		return nil
	}
	if repo == nil || c.Repo != repo.key() {
		delete(c.Tabs, tabRepo)
	}
	return c.Tabs
}

// saveListCache stores one tab's full list, keeping the others.
func saveListCache(t tab, prs []pullRequest, repo *repoRef) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	var c listCache
	if data, err := os.ReadFile(listCachePath()); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if c.Tabs == nil {
		c.Tabs = map[tab][]pullRequest{}
	}
	if t == tabRepo {
		if repo == nil {
			return
		}
		c.Repo = repo.key()
	}
	c.Tabs[t], c.Saved = prs, nowFn()
	data, err := json.Marshal(c)
	if err != nil || os.MkdirAll(stateDir(), 0o700) != nil {
		return
	}
	tmp := listCachePath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, listCachePath())
	}
}
