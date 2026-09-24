package main

import (
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The repo tab shows the repo of the space the popup was opened from; ctrl+t
// (or a click on that tab when you're on it) picks another for this popup.
// Next time it opens on the space's repo again.

// repoChoice is one repo the picker offers, and why it's there.
type repoChoice struct {
	repo repoRef
	note string
}

type repoChoicesMsg []repoChoice

// loadRepoChoices gathers the repos worth offering: this space's, the ones
// picked lately, every herdr space's, and those your PRs are in. herdr and
// git are asked off the UI's goroutine; the lists are read here.
func (m model) loadRepoChoices() tea.Cmd {
	var fromLists []repoChoice
	for _, t := range []tab{tabMine, tabReview} {
		note := map[tab]string{tabMine: "your PRs", tabReview: "review requested"}[t]
		for _, pr := range m.prs[t] {
			fromLists = append(fromLists, repoChoice{pr.repo(), note})
		}
	}
	here, cfg, ctx := m.home, m.cfg, m.ctx
	if d, ok := m.client.(*demoSource); ok {
		return func() tea.Msg {
			var out []repoChoice
			if here != nil {
				out = append(out, repoChoice{*here, "this space"})
			}
			for _, r := range d.repos() {
				out = append(out, repoChoice{r, "a herdr space"})
			}
			return repoChoicesMsg(dedupeChoices(append(out, fromLists...)))
		}
	}
	return func() tea.Msg {
		var out []repoChoice
		if here != nil {
			out = append(out, repoChoice{*here, "this space"})
		}
		for _, k := range readPrefs().RecentRepos {
			if r, ok := parseRepoKey(k); ok {
				out = append(out, repoChoice{r, "recent"})
			}
		}
		if wss, err := listWorkspacesFn(); err == nil {
			for _, w := range wss {
				if w.Worktree == nil || w.Worktree.RepoRoot == "" {
					continue
				}
				if r, ok := repoOf(ctx, w.Worktree.RepoRoot); ok && cfg.knownHost(r.Host) {
					out = append(out, repoChoice{r, "a herdr space"})
				}
			}
		}
		return repoChoicesMsg(dedupeChoices(append(out, fromLists...)))
	}
}

// dedupeChoices keeps each repo once, where it first appears.
func dedupeChoices(cs []repoChoice) []repoChoice {
	seen := map[string]bool{}
	var out []repoChoice
	for _, c := range cs {
		if c.repo.valid() && !seen[c.repo.key()] {
			seen[c.repo.key()] = true
			out = append(out, c)
		}
	}
	return out
}

// repoMenu is the picker: the choices, filtered as you type, plus whatever
// owner/repo (or host/owner/repo) you type that isn't among them.
func (m model) repoMenu(choices []repoChoice) *menu {
	mn := &menu{title: "Pull requests of which repo?", filterable: true}
	for _, c := range choices {
		c := c
		label := c.repo.String()
		if c.repo.Host != "github.com" {
			label = c.repo.Host + "/" + label
		}
		note := c.note
		if m.repo != nil && c.repo.same(*m.repo) {
			note += " · showing"
		}
		mn.items = append(mn.items, menuItem{label: label, detail: note, run: func(m model) (tea.Model, tea.Cmd) {
			return m.switchRepo(c.repo)
		}})
	}
	host := m.cfg.Hosts[0]
	if m.repo != nil {
		host = m.repo.Host
	}
	known := map[string]bool{}
	for _, c := range choices {
		known[c.repo.key()] = true
	}
	mn.typed = func(q string) *menuItem {
		r, ok := typedRepo(strings.TrimSpace(q), host)
		if !ok || known[r.key()] || !m.cfg.knownHost(r.Host) {
			return nil
		}
		return &menuItem{label: r.String(), detail: "as typed", run: func(m model) (tea.Model, tea.Cmd) {
			return m.switchRepo(r)
		}}
	}
	return mn
}

var repoNamePart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// typedRepo reads "owner/repo" (on host) or "host/owner/repo".
func typedRepo(q, host string) (repoRef, bool) {
	parts := strings.Split(q, "/")
	if len(parts) == 3 && strings.Contains(parts[0], ".") {
		host, parts = strings.ToLower(parts[0]), parts[1:]
	}
	if len(parts) != 2 || !repoNamePart.MatchString(parts[0]) || !repoNamePart.MatchString(parts[1]) {
		return repoRef{}, false
	}
	return repoRef{Host: host, Owner: parts[0], Name: parts[1]}, true
}

func parseRepoKey(k string) (repoRef, bool) {
	parts := strings.Split(k, "/")
	if len(parts) != 3 {
		return repoRef{}, false
	}
	r := repoRef{Host: parts[0], Owner: parts[1], Name: parts[2]}
	return r, r.valid()
}

// openRepoPicker opens the picker once the choices are gathered.
func (m model) openRepoPicker() (tea.Model, tea.Cmd) {
	m.flash, m.err = "", ""
	return m, m.loadRepoChoices()
}

// switchRepo points the repo tab at r for this popup and loads its PRs.
func (m model) switchRepo(r repoRef) (tea.Model, tea.Cmd) {
	m.repo = &r
	switch c := m.client.(type) {
	case *githubSource:
		m.client = newGitHubSource(m.cfg, &r)
	case *demoSource:
		c.setHome(r)
	}
	m.prs[tabRepo], m.loaded[tabRepo], m.stale[tabRepo], m.paging[tabRepo], m.tabErr[tabRepo] = nil, false, false, false, ""
	m.tab, m.screen, m.cur, m.curDetail = tabRepo, screenList, nil, nil
	m.cursor, m.offset, m.mode = 0, 0, modeLoading
	if !m.demo {
		rememberRecentRepo(r)
	}
	return m, tea.Batch(m.spin.Tick, m.loadTab(tabRepo, ""))
}
