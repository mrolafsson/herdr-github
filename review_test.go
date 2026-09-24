package main

// Regression tests for the adversarial review's findings, one per defect.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A same-repo branch "alice/fix" and alice's fork PR from "fix" share a
// local branch name. Neither PR may open or remove the other's worktree.
func TestForkAndRepoBranchNamesCantCollide(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	ctx := context.Background()
	work := filepath.Join(f.root, "work")
	sh(t, work, "git", "push", "-q", "origin", "main:refs/heads/alice/fix")

	same := samePR("github.com")
	same.Number, same.HeadRefName = 7, "alice/fix"
	res, created, err := openWorktree(ctx, withDefaults(config{}), same, f.clone)
	if err != nil || !created {
		t.Fatalf("#7: created=%v err=%v", created, err)
	}
	fork := forkPR()
	if _, _, err := openWorktree(ctx, withDefaults(config{}), fork, f.clone); err == nil || !strings.Contains(err.Error(), "#7") {
		t.Fatalf("#5 should refuse #7's branch, got %v", err)
	}
	if err := removeWorktree(ctx, fork, f.clone); err == nil {
		t.Fatal("#5's clean-up removed #7's worktree")
	}
	if _, err := os.Stat(res.Worktree.Path); err != nil {
		t.Fatal("#7's worktree is gone")
	}
	// And the other way round: a branch of yours with the fork's name.
	sh(t, f.clone, "git", "branch", "bob/fix", "main")
	bob := forkPR()
	bob.HeadRepositoryOwner = &actor{"bob"}
	if _, _, err := openWorktree(ctx, withDefaults(config{}), bob, f.clone); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("a branch of yours named like the fork's: %v", err)
	}
}

// The main checkout is the repo, never "the PR's worktree", even when it
// happens to be on the PR's branch.
func TestMainCheckoutIsNotAPRWorktree(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	sh(t, f.clone, "git", "switch", "-q", "feat")
	res, _, err := openWorktree(context.Background(), withDefaults(config{}), samePR("github.com"), f.clone)
	if err == nil && res.Worktree.Path == f.clone {
		t.Fatal("opened the main checkout as the PR's worktree")
	}
	if err := removeWorktree(context.Background(), samePR("github.com"), f.clone); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("remove should not touch the main checkout: %v", err)
	}
	if _, err := os.Stat(f.clone); err != nil {
		t.Fatal(err)
	}
}

// A checkout mapped under "repos" because its remote uses an SSH host alias
// still gets worktrees.
func TestMappedCheckoutWithSSHAlias(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	sh(t, f.clone, "git", "config", "url."+f.origin+".insteadOf", "git@github-work:o/r.git")
	sh(t, f.clone, "git", "remote", "set-url", "origin", "git@github-work:o/r.git")
	remoteCache.Delete(f.clone)
	cfg := withDefaults(config{Repos: map[string]string{"o/r": f.clone}})
	dir, err := findCheckout(context.Background(), cfg, repoRef{"github.com", "o", "r"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := openWorktree(context.Background(), withDefaults(config{}), samePR("github.com"), dir); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

// The kickoff prompt reaches the child even when this process moves on (and
// exits) straight after starting it.
func TestStartWithInputDeliversAllOfStdin(t *testing.T) {
	out := filepath.Join(t.TempDir(), "got")
	prompt := strings.Repeat("prompt text ", 4000) // ~48KB
	cmd := exec.Command("sh", "-c", "sleep 0.2; cat > "+out)
	if err := startWithInput(cmd, prompt); err != nil {
		t.Fatal(err)
	}
	// No waiting on the pipe's copy: the write is done already.
	_ = cmd.Wait()
	got, _ := os.ReadFile(out)
	if string(got) != prompt {
		t.Fatalf("child read %d of %d bytes", len(got), len(prompt))
	}
}

func spaceIn(f fixture, dir string) workspaceInfo { return wsAt("w1", dir, f.clone, nil) }

// A branch made from origin/main tracks main: its PR is under its own name.
// An unpushed branch belongs to the repo's owner, not any fork with a
// same-named branch.
func TestLabelHeadForTrackingAndUnpushedBranches(t *testing.T) {
	f := gitFixture(t)
	ctx := context.Background()
	cfg := withDefaults(config{})
	sh(t, f.clone, "git", "switch", "-q", "-c", "newwork", "--track", "origin/main")
	sb, ok := spaceBranchOf(ctx, cfg, spaceIn(f, f.clone))
	if !ok || sb.head != (branchHead{"newwork", "o"}) {
		t.Fatalf("tracking main: %+v %v", sb.head, ok)
	}
	sh(t, f.clone, "git", "switch", "-q", "-c", "patch-1", "main")
	if sb, _ := spaceBranchOf(ctx, cfg, spaceIn(f, f.clone)); sb.head != (branchHead{"patch-1", "o"}) {
		t.Fatalf("unpushed: %+v", sb.head)
	}
	stranger := []prStatus{{Number: 9, State: "OPEN", HeadRepositoryOwner: &actor{"stranger"}}}
	if _, ok := pickPR(stranger, "o"); ok {
		t.Fatal("a stranger's fork PR was taken for your unpushed branch")
	}
}

// While GitHub fails, events don't each ask it again.
func TestTickBacksOffAfterAFailure(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	feat, _, _ := openWorktree(context.Background(), withDefaults(config{}), samePR("github.com"), f.clone)
	old := listWorkspacesFn
	listWorkspacesFn = listWorkspaces
	t.Cleanup(func() { listWorkspacesFn = old })
	h.workspaces = []workspaceInfo{wsAt("w1", feat.Worktree.Path, f.clone, nil)}
	asks := 0
	oldLookup := branchLookupFn
	t.Cleanup(func() { branchLookupFn = oldLookup })
	branchLookupFn = func(context.Context, repoRef, []branchHead) (map[branchHead]prStatus, error) {
		asks++
		return nil, errors.New("GitHub: HTTP 502")
	}
	start, oldNow := nowFn(), nowFn
	t.Cleanup(func() { nowFn = oldNow })
	for _, d := range []time.Duration{0, 5 * time.Second, 30 * time.Second, 59 * time.Second} {
		nowFn = func() time.Time { return start.Add(d) }
		_ = tick(context.Background(), withDefaults(config{}), false)
	}
	if asks != 1 {
		t.Fatalf("asked GitHub %d times inside a minute of failures", asks)
	}
	nowFn = func() time.Time { return start.Add(65 * time.Second) }
	_ = tick(context.Background(), withDefaults(config{}), false)
	if asks != 2 {
		t.Fatal("never tried again")
	}
}

// A space that moved to another branch while GitHub was unreachable loses
// the old branch's label rather than keep a PR that isn't its own.
func TestOfflineBranchSwitchDropsTheOldLabel(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	feat, _, _ := openWorktree(context.Background(), withDefaults(config{}), samePR("github.com"), f.clone)
	old := listWorkspacesFn
	listWorkspacesFn = listWorkspaces
	t.Cleanup(func() { listWorkspacesFn = old })
	h.workspaces = []workspaceInfo{wsAt("w1", feat.Worktree.Path, f.clone, nil)}
	oldLookup := branchLookupFn
	t.Cleanup(func() { branchLookupFn = oldLookup })
	branchLookupFn = func(_ context.Context, _ repoRef, heads []branchHead) (map[branchHead]prStatus, error) {
		return map[branchHead]prStatus{heads[0]: {Number: 12, State: "OPEN"}}, nil
	}
	_ = tick(context.Background(), withDefaults(config{}), true)
	if r := reportFor(h.takeReports(), "workspace_id", "w1"); r == nil {
		t.Fatal("not labelled")
	}
	h.workspaces[0].Tokens = map[string]string{"pr": "#12", "pr_badge": "#12", "pr_state": "open"}
	sh(t, feat.Worktree.Path, "git", "switch", "-q", "-c", "other")
	branchLookupFn = func(context.Context, repoRef, []branchHead) (map[branchHead]prStatus, error) {
		return nil, errors.New("offline")
	}
	_ = tick(context.Background(), withDefaults(config{}), true)
	r := reportFor(h.takeReports(), "workspace_id", "w1")
	if r == nil || r["tokens"].(map[string]any)["pr"] != nil {
		t.Fatalf("the old branch's label stayed: %v", r)
	}
}

// One error in GitHub's answer (a SAML-protected org, say) doesn't throw
// away the rest of it.
func TestPartialAnswersAreKept(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   map[string]any{"search": map[string]any{"pageInfo": map[string]any{}, "nodes": []any{prNode(1), nil}}},
			"errors": []any{map[string]any{"type": "FORBIDDEN", "message": "Resource protected by organization SAML enforcement."}},
		})
	})
	s := newGitHubSource(withDefaults(config{}), nil)
	p, err := s.list(context.Background(), tabMine, "")
	if len(p.prs) != 1 || err == nil || !strings.Contains(err.Error(), "SAML") {
		t.Fatalf("%d PRs, err %v", len(p.prs), err)
	}
	// A mutation's error is never partial.
	if err := newClient("github.com").setDraft(context.Background(), "PR_1", true); err == nil || isPartial(err) {
		t.Fatalf("mutation: %v", err)
	}
}

// The repo tab, opened outside a GitHub repo, has nothing coming: it mustn't
// wait for ever.
func TestRepoTabOutsideARepoDoesntHang(t *testing.T) {
	m := newModel(context.Background(), withDefaults(config{}), &pagedSource{}, "", nil)
	m.width, m.height, m.tab = 100, 30, tabRepo
	m = m.settle()
	if m.mode != modeList || !strings.Contains(plain(m), "Pick a repo") {
		t.Fatalf("mode %v:\n%s", m.mode, plain(m))
	}
	next, _ := m.refreshAll()
	if next.mode != modeList {
		t.Fatal("after sign-in it hangs again")
	}
}

func TestFailedLoadStopsTheSpinner(t *testing.T) {
	m := newModel(context.Background(), withDefaults(config{}), &pagedSource{err: errors.New("GitHub: offline")}, "", nil)
	m.width, m.height = 100, 30
	m = m.useCache(map[tab][]pullRequest{tabMine: prsNumbered(5)})
	m = drive(m, m.loadAll())
	if strings.Contains(plain(m), "updating") {
		t.Fatalf("still 'updating' after the load failed:\n%s", plain(m))
	}
}

// d goes by the PR as just loaded, and waits for it after a change.
func TestDraftWaitsForTheFreshPR(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter")
	m.curDetail = nil
	m = press(m, "d")
	if m.mode == modeBusy || m.flash != "Still loading…" {
		t.Fatalf("d without a loaded PR: mode %v flash %q", m.mode, m.flash)
	}
}

// ── round 2 ───────────────────────────────────────────────────────────────────

// The repo list opened while a repo loads takes keys, and esc closes it,
// not the popup.
func TestRepoListWorksWhileARepoLoads(t *testing.T) {
	m := demoModel(t, tabRepo)
	m.mode = modeLoading // as if the repo were still loading
	next, cmd := m.Update(keyMsg("ctrl+t"))
	m = drive(next.(model), cmd)
	if m.menu == nil {
		t.Fatal("ctrl+t while loading should open the list")
	}
	before := m.menuCursor
	m = press(m, "down")
	if m.menuCursor == before {
		t.Fatal("keys ignored in the list while loading")
	}
	next, cmd = m.Update(keyMsg("esc"))
	if quits(cmd) || next.(model).menu != nil {
		t.Fatal("esc should close the list, not the popup")
	}
}

// Hovering a scrolled list never scrolls it under the pointer.
func TestHoverDoesntScrollTheMenu(t *testing.T) {
	m := demoModel(t, tabRepo)
	m.height = 14 // room for a few items only
	mn := &menu{id: "x", title: "t", filterable: true}
	for i := 0; i < 40; i++ {
		mn.items = append(mn.items, menuItem{label: fmt.Sprintf("item %d", i), run: func(m model) (tea.Model, tea.Cmd) { return m, nil }})
	}
	m = m.openMenu(mn)
	for i := 0; i < 20; i++ {
		m = press(m, "down")
	}
	start := m.menuStart(m.menuRoom())
	top := listTop + menuTop
	for y := top; y < top+3; y++ {
		next, _ := m.Update(tea.MouseMsg{X: 5, Y: y, Action: tea.MouseActionMotion})
		m = next.(model)
		if got := m.menuStart(m.menuRoom()); got != start {
			t.Fatalf("hovering line %d scrolled the menu from %d to %d", y, start, got)
		}
		if m.menuCursor != start+(y-top) {
			t.Fatalf("hover on line %d put the cursor on %d, not the item drawn there (%d)", y, m.menuCursor, start+(y-top))
		}
	}
}

// Repo choices arriving after you've opened a PR don't cover it.
func TestLateRepoChoicesDontCoverAPR(t *testing.T) {
	m := demoModel(t, tabRepo)
	late := m.loadRepoChoices()()
	m = press(m, "enter") // into #482
	next, _ := m.Update(late)
	if next.(model).menu != nil {
		t.Fatal("the repo list opened over the PR screen")
	}
}

// A→B→A: pages asked for during the first A don't land in the second.
func TestRepoSwitchBackDropsOldPages(t *testing.T) {
	m := demoModel(t, tabRepo)
	a := *m.repo
	stale := listMsg{tab: tabRepo, repo: a.key(), pick: m.picks, cursor: "p2", prs: prsNumbered(21, 22), gen: m.gen}
	next, _ := m.switchRepo(repoRef{"github.com", "halcyon", "sync-server"})
	next, _ = next.(model).switchRepo(a)
	m = next.(model)
	next, _ = m.Update(stale)
	if strings.Contains(plain(next.(model)), "PR 21") {
		t.Fatal("a page from before the switches was shown")
	}
}

// A PR closed and reopened from the same branch is the same head: its new
// number takes the branch over.
func TestReopenedPRTakesOverItsBranch(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	cfg := withDefaults(config{})
	if _, _, err := openWorktree(context.Background(), cfg, samePR("github.com"), f.clone); err != nil {
		t.Fatal(err)
	}
	again := samePR("github.com")
	again.Number = 13
	if _, created, err := openWorktree(context.Background(), cfg, again, f.clone); err != nil || created {
		t.Fatalf("reopened as #13: created=%v err=%v", created, err)
	}
	if err := removeWorktree(context.Background(), again, f.clone); err != nil {
		t.Fatalf("clean-up for #13: %v", err)
	}
}

// A fork's branch set up before branches were marked (it pulls from the
// fork) is still that PR's.
func TestUnmarkedForkBranchSetUpForTheForkIsAccepted(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	sh(t, f.clone, "git", "fetch", "-q", "origin", "refs/pull/5/head:alice/fix")
	sh(t, f.clone, "git", "config", "branch.alice/fix.remote", "https://github.com/alice/r.git")
	sh(t, f.clone, "git", "config", "branch.alice/fix.merge", "refs/heads/fix")
	if _, _, err := openWorktree(context.Background(), withDefaults(config{}), forkPR(), f.clone); err != nil {
		t.Fatalf("pre-marker fork branch refused: %v", err)
	}
}

// o/r on another GitHub you use is a different repo: never its checkout.
func TestAnotherGitHubsRepoIsNotAnAlias(t *testing.T) {
	f := gitFixture(t)
	h := &fakeHerdr{}
	h.install(t)
	cfg := withDefaults(config{Hosts: []string{"github.com", "ghe.example.com"}})
	pr := forkPR()
	pr.Host = "ghe.example.com"
	if _, _, err := openWorktree(context.Background(), cfg, pr, f.clone); err == nil || !strings.Contains(err.Error(), "no remote") {
		t.Fatalf("a github.com checkout was used for a GHE PR: %v", err)
	}
}

// A partial answer about branches is a failure: labels stay as they are.
func TestPartialBranchAnswerKeepsLabels(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   map[string]any{"repository": map[string]any{"b0": nil}},
			"errors": []any{map[string]any{"message": "Something went wrong while executing your query."}},
		})
	})
	_, err := newClient("github.com").prsForBranches(context.Background(), repoRef{"github.com", "o", "r"}, []branchHead{{"feat", "o"}})
	if err == nil {
		t.Fatal("a partial answer about branches should be an error")
	}
}
