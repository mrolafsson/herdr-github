package main

// Regression tests for the adversarial review's findings, one per defect.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	res, created, err := openWorktree(ctx, same, f.clone)
	if err != nil || !created {
		t.Fatalf("#7: created=%v err=%v", created, err)
	}
	fork := forkPR()
	if _, _, err := openWorktree(ctx, fork, f.clone); err == nil || !strings.Contains(err.Error(), "#7") {
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
	if _, _, err := openWorktree(ctx, bob, f.clone); err == nil || !strings.Contains(err.Error(), "rename") {
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
	res, _, err := openWorktree(context.Background(), samePR("github.com"), f.clone)
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
	if _, created, err := openWorktree(context.Background(), samePR("github.com"), dir); err != nil || !created {
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
	feat, _, _ := openWorktree(context.Background(), samePR("github.com"), f.clone)
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
	feat, _, _ := openWorktree(context.Background(), samePR("github.com"), f.clone)
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
	if m.mode != modeList || !strings.Contains(plain(m), "Open this from a space inside a GitHub repo") {
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
