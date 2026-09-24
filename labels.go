package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The sidebar labels. On herdr's events (and at startup) `tick` looks at every
// space that is a git checkout, works out the pull request for the branch it's
// on, and reports it as display-only tokens on the space and on each agent
// pane in it. You show them by adding e.g. "$pr_badge" to a sidebar row.
//
// It is cheap when nothing is due: herdr fires events often, so a tick within
// a few seconds of the last one does nothing, and GitHub is only asked about a
// repo whose answer is older than label_refresh_seconds.

const (
	tokenSource = "herdr-github"
	// Tokens outlive a quiet spell but not a stale plugin: each tick that
	// finds them older than half this re-reports them.
	tokenTTL      = 2 * time.Hour
	tickCooldown  = 3 * time.Second
	branchesPerQ  = 20 // branches looked up per GraphQL query
	tickTimeLimit = 45 * time.Second
)

// tokenNames are every token this plugin sets, so a space whose PR went away
// has all of them cleared.
var tokenNames = []string{"pr", "pr_badge", "pr_state", "pr_checks", "pr_review"}

// labelState is labels.json: what GitHub last said per branch, and when the
// tokens were last sent.
type labelState struct {
	LastTick time.Time              `json:"last_tick"`
	Branches map[string]branchEntry `json:"branches"` // host/owner/repo + "\x00" + owner:branch
	Reported map[string]reported    `json:"reported"` // space or pane ID → what was sent
}

type branchEntry struct {
	Fetched time.Time `json:"fetched"`
	PR      *prStatus `json:"pr"` // nil: no PR for this branch
}

type reported struct {
	Tokens map[string]string `json:"tokens"`
	At     time.Time         `json:"at"`
}

func labelStatePath() string { return filepath.Join(stateDir(), "labels.json") }

func readLabelState() labelState {
	var s labelState
	if data, err := os.ReadFile(labelStatePath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Branches == nil {
		s.Branches = map[string]branchEntry{}
	}
	if s.Reported == nil {
		s.Reported = map[string]reported{}
	}
	return s
}

func writeLabelState(s labelState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := labelStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, labelStatePath())
}

// badgeTokens are the tokens for a PR; nil means "no PR": clear them all.
func badgeTokens(s *prStatus) map[string]string {
	if s == nil {
		return nil
	}
	num := fmt.Sprintf("#%d", s.Number)
	t := map[string]string{"pr": num}
	state := "open"
	switch {
	case s.State == "MERGED":
		state = "merged"
	case s.State == "CLOSED":
		state = "closed"
	case s.IsDraft:
		state = "draft"
	}
	t["pr_state"] = state
	checks := checkGlyph(s.checks())
	if checks != "" && s.State == "OPEN" {
		t["pr_checks"] = checks
	}
	review := ""
	switch s.ReviewDecision {
	case "APPROVED":
		review = "approved"
	case "CHANGES_REQUESTED":
		review = "changes requested"
	case "REVIEW_REQUIRED":
		review = "needs review"
	}
	if review != "" && s.State == "OPEN" {
		t["pr_review"] = review
	}

	// The badge: the number and what matters most, briefly.
	parts := []string{num}
	switch state {
	case "merged", "closed", "draft":
		parts = append(parts, state)
	}
	if s.State == "OPEN" {
		if checks != "" {
			parts = append(parts, checks)
		}
		switch {
		case s.Mergeable == "CONFLICTING":
			parts = append(parts, "conflicts")
		case s.ReviewDecision == "CHANGES_REQUESTED":
			parts = append(parts, "changes")
		case s.ReviewDecision == "APPROVED" && !s.IsDraft:
			parts = append(parts, "approved")
		}
		if s.AutoMergeRequest != nil {
			parts = append(parts, "auto")
		}
	}
	t["pr_badge"] = strings.Join(parts, " ")
	return t
}

// checkGlyph folds a check rollup state into ✓ passed, ✗ failed, ● running.
func checkGlyph(state string) string {
	switch state {
	case "SUCCESS":
		return "✓"
	case "FAILURE", "ERROR":
		return "✗"
	case "PENDING", "EXPECTED":
		return "●"
	}
	return ""
}

// tokenParams turns tokens into report_metadata's map: every name we own,
// with null for the ones not set now, so stale ones are cleared.
func tokenParams(t map[string]string) map[string]any {
	out := map[string]any{}
	for _, n := range tokenNames {
		if v, ok := t[n]; ok {
			out[n] = v
		} else {
			out[n] = nil
		}
	}
	return out
}

func sameTokens(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// spaceBranch is what tick learns about one space from git.
type spaceBranch struct {
	workspaceID string
	repo        repoRef
	head        branchHead
}

func (sb spaceBranch) cacheKey() string {
	return sb.repo.key() + "\x00" + strings.ToLower(sb.head.Owner) + ":" + sb.head.Branch
}

// spaceBranchOf works out which repo and head branch a space's checkout is
// on. The head is what the branch pushes to: its upstream's branch name, and
// for a fork's PR (checked out by this plugin or gh) the fork's owner.
func spaceBranchOf(ctx context.Context, cfg config, w workspaceInfo) (spaceBranch, bool) {
	if w.Worktree == nil || w.Worktree.CheckoutPath == "" {
		return spaceBranch{}, false
	}
	dir := w.Worktree.CheckoutPath
	branch, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return spaceBranch{}, false // detached, or not a checkout any more
	}
	root := w.Worktree.RepoRoot
	if root == "" {
		root = dir
	}
	repo, ok := repoOf(ctx, root)
	if !ok || !cfg.knownHost(repo.Host) {
		return spaceBranch{}, false
	}
	// The default branch is where PRs go, not where they come from.
	if def, err := git(ctx, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.TrimPrefix(def, "origin/") == branch {
		return spaceBranch{}, false
	}
	if branch == "main" || branch == "master" {
		return spaceBranch{}, false
	}
	head := branchHead{Branch: branch}
	if merge, err := git(ctx, dir, "config", "--get", "branch."+branch+".merge"); err == nil && strings.HasPrefix(merge, "refs/heads/") {
		head.Branch = strings.TrimPrefix(merge, "refs/heads/")
	}
	if rem, err := git(ctx, dir, "config", "--get", "branch."+branch+".remote"); err == nil && rem != "" && rem != "." {
		if r, ok := parseRemote(rem); ok { // a URL: a fork's, set by us or gh
			head.Owner = r.Owner
		} else {
			for _, x := range remotesOf(ctx, root) {
				if x.Name == rem && x.Repo.valid() {
					head.Owner = x.Repo.Owner
				}
			}
		}
	}
	return spaceBranch{workspaceID: w.WorkspaceID, repo: repo, head: head}, true
}

// Seams for tests.
var (
	branchLookupFn = func(ctx context.Context, r repoRef, heads []branchHead) (map[branchHead]prStatus, error) {
		return newClient(r.Host).prsForBranches(ctx, r, heads)
	}
	nowFn = time.Now
)

// tick refreshes the sidebar labels. force skips the cooldown and asks
// GitHub again whatever the cache's age (the popup does this after changing
// a PR).
func tick(ctx context.Context, cfg config, force bool) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	// One tick at a time; one arriving while another runs has nothing to add.
	lock, err := os.OpenFile(filepath.Join(stateDir(), "tick.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !force {
			return nil
		}
		// A forced tick must see the change it was asked for: wait its turn.
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	st := readLabelState()
	now := nowFn()
	if !force && now.Sub(st.LastTick) < tickCooldown {
		return nil
	}
	st.LastTick = now
	ctx, cancel := context.WithTimeout(ctx, tickTimeLimit)
	defer cancel()

	wss, err := listWorkspacesFn()
	if err != nil {
		return err
	}
	if !cfg.labelsOn() {
		return clearAll(st, wss)
	}

	// What each space is on.
	spaces := map[string]spaceBranch{}
	for _, w := range wss {
		if sb, ok := spaceBranchOf(ctx, cfg, w); ok {
			spaces[w.WorkspaceID] = sb
		}
	}

	// Ask GitHub about the branches whose answer is due, a repo at a time.
	fresh := time.Duration(cfg.LabelRefreshSeconds) * time.Second
	due := map[string][]spaceBranch{} // repo key → spaces
	for _, sb := range spaces {
		e, ok := st.Branches[sb.cacheKey()]
		if force || !ok || now.Sub(e.Fetched) >= fresh {
			due[sb.repo.key()] = append(due[sb.repo.key()], sb)
		}
	}
	var lookupErr error
	for _, sbs := range due {
		repo := sbs[0].repo
		seen := map[branchHead]bool{}
		var heads []branchHead
		for _, sb := range sbs {
			if !seen[sb.head] {
				seen[sb.head] = true
				heads = append(heads, sb.head)
			}
		}
		for i := 0; i < len(heads); i += branchesPerQ {
			batch := heads[i:min(len(heads), i+branchesPerQ)]
			found, err := branchLookupFn(ctx, repo, batch)
			if err != nil {
				// Signed out or offline: keep showing what we knew.
				lookupErr = err
				continue
			}
			for _, h := range batch {
				e := branchEntry{Fetched: now}
				if s, ok := found[h]; ok {
					s := s
					e.PR = &s
				}
				st.Branches[spaceBranch{repo: repo, head: h}.cacheKey()] = e
			}
		}
	}

	// Report: to each space, and each agent pane in it.
	panes, _ := listPanes("")
	byWS := map[string][]paneInfo{}
	for _, p := range panes {
		byWS[p.WorkspaceID] = append(byWS[p.WorkspaceID], p)
	}
	live := map[string]bool{}
	for _, w := range wss {
		live[w.WorkspaceID] = true
		for _, p := range byWS[w.WorkspaceID] {
			live[p.PaneID] = true
		}
		var want map[string]string
		if sb, ok := spaces[w.WorkspaceID]; ok {
			e, known := st.Branches[sb.cacheKey()]
			if !known {
				continue // GitHub couldn't say: leave what's shown
			}
			want = badgeTokens(e.PR)
		}
		st.report("workspace", w.WorkspaceID, want, w.Tokens, now)
		for _, p := range byWS[w.WorkspaceID] {
			pw := want
			if p.Agent == "" {
				pw = nil // only agents are labelled; a shell keeps its own
			}
			st.report("pane", p.PaneID, pw, p.Tokens, now)
		}
	}
	// Forget spaces and panes that are gone, and branches nobody is on.
	for id := range st.Reported {
		if !live[id] {
			delete(st.Reported, id)
		}
	}
	inUse := map[string]bool{}
	for _, sb := range spaces {
		inUse[sb.cacheKey()] = true
	}
	for k := range st.Branches {
		if !inUse[k] {
			delete(st.Branches, k)
		}
	}
	if err := writeLabelState(st); err != nil {
		return err
	}
	return lookupErr
}

// report sends tokens to a space or pane when they differ from what it shows
// (current, as herdr lists it) or are getting old.
func (st *labelState) report(kind, id string, want, current map[string]string, now time.Time) {
	ours := map[string]string{}
	for _, n := range tokenNames {
		if v, ok := current[n]; ok {
			ours[n] = v
		}
	}
	last, sent := st.Reported[id]
	switch {
	case want == nil && len(ours) == 0:
		delete(st.Reported, id)
		return
	case sameTokens(want, ours) && sent && now.Sub(last.At) < tokenTTL/2:
		return
	}
	params := map[string]any{"source": tokenSource, "tokens": tokenParams(want)}
	if want != nil {
		params["ttl_ms"] = tokenTTL.Milliseconds()
	}
	method := "workspace.report_metadata"
	if kind == "pane" {
		method, params["pane_id"] = "pane.report_metadata", id
	} else {
		params["workspace_id"] = id
	}
	if err := herdrCall(method, params, nil); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-github: labels:", err)
		return
	}
	if want == nil {
		delete(st.Reported, id)
	} else {
		st.Reported[id] = reported{Tokens: want, At: now}
	}
}

// clearAll removes every label, for when they're turned off.
func clearAll(st labelState, wss []workspaceInfo) error {
	panes, _ := listPanes("")
	now := nowFn()
	for _, w := range wss {
		st.report("workspace", w.WorkspaceID, nil, w.Tokens, now)
	}
	for _, p := range panes {
		st.report("pane", p.PaneID, nil, p.Tokens, now)
	}
	st.Branches = map[string]branchEntry{}
	return writeLabelState(st)
}

// isQuietTickErr says whether a tick failure is expected (no gh sign-in,
// offline) and not worth a line in herdr's plugin log on every event.
func isQuietTickErr(err error) bool {
	return errors.Is(err, errSignedOut) || errors.Is(err, context.DeadlineExceeded)
}
