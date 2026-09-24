package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// prefs.json remembers small choices between popups: the merge method you
// last used in each repo, and the tab you were last on.
type prefs struct {
	MergeMethods map[string]string `json:"merge_methods"` // repo key → MERGE, SQUASH, REBASE
	Tab          *int              `json:"tab"`
}

func prefsPath() string { return filepath.Join(stateDir(), "prefs.json") }

func readPrefs() prefs {
	var p prefs
	if data, err := os.ReadFile(prefsPath()); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	if p.MergeMethods == nil {
		p.MergeMethods = map[string]string{}
	}
	return p
}

// updatePrefs is best effort: losing a preference only costs a keypress.
func updatePrefs(change func(*prefs)) {
	p := readPrefs()
	change(&p)
	data, err := json.Marshal(p)
	if err != nil || os.MkdirAll(stateDir(), 0o700) != nil {
		return
	}
	tmp := prefsPath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, prefsPath())
	}
}

// preferredMethod is the method to offer first: yours for this repo, else
// the one GitHub remembers you using (viewerDefaultMergeMethod).
func preferredMethod(r repoRef, githubDefault string) string {
	if m := readPrefs().MergeMethods[r.key()]; m != "" {
		return m
	}
	return githubDefault
}

func rememberMethod(r repoRef, method string) {
	updatePrefs(func(p *prefs) { p.MergeMethods[r.key()] = method })
}

func rememberTab(t tab) {
	updatePrefs(func(p *prefs) { n := int(t); p.Tab = &n })
}

func lastTab() (tab, bool) {
	if p := readPrefs(); p.Tab != nil && *p.Tab >= 0 && *p.Tab <= int(tabRepo) {
		return tab(*p.Tab), true
	}
	return tabMine, false
}
