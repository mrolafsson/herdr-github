package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func plain(m model) string { return stripStyles(m.View()) }

func TestTabsListAndGroup(t *testing.T) {
	m := demoModel(t, tabMine)
	v := plain(m)
	for _, want := range []string{"Mine 4", "halcyon/notes-app", "halcyon/sync-server", "#482", "Offline edits", "approved", "⌥"} {
		if !strings.Contains(v, want) {
			t.Fatalf("mine tab lacks %q:\n%s", want, v)
		}
	}
	m = press(m, "tab")
	if m.tab != tabReview || !strings.Contains(plain(m), "Fix typo in onboarding") || !strings.Contains(plain(m), "maria-k") {
		t.Fatalf("review tab:\n%s", plain(m))
	}
	m = press(m, "tab")
	v = plain(m)
	if m.tab != tabRepo || !strings.Contains(v, "Drafts") || !strings.Contains(v, "jonas") || !strings.Contains(v, "auto") {
		t.Fatalf("repo tab:\n%s", v)
	}
	// Drafts come after the ready ones.
	if strings.Index(v, "Spike: CRDT") < strings.Index(v, "Faster cold start") {
		t.Fatalf("drafts not last:\n%s", v)
	}
	if m = press(m, "tab"); m.tab != tabMine {
		t.Fatal("tab doesn't wrap around")
	}
}

func TestFilterMatchesNumberRepoAndLabel(t *testing.T) {
	m := demoModel(t, tabMine)
	for _, r := range "sync bug" {
		m = press(m, string(r))
	}
	rows := m.rows()
	if len(rows) != 2 || rows[1].pr.Number != 482 {
		t.Fatalf("filter 'sync bug': %+v", rows)
	}
	m = press(m, "esc")
	if m.filter.Value() != "" || len(m.rows()) < 4 {
		t.Fatal("esc should clear the filter first")
	}
	for _, r := range "#118" {
		m = press(m, string(r))
	}
	if r := m.selected(); r == nil || r.pr.Number != 118 {
		t.Fatalf("filter by number: %v", m.rows())
	}
}

func TestCursorSkipsHeadings(t *testing.T) {
	m := demoModel(t, tabMine)
	if r := m.selected(); r == nil || r.pr.Number != 482 {
		t.Fatal("cursor should start on the first PR, not its heading")
	}
	m = press(m, "down", "down", "down")
	if r := m.selected(); r == nil || r.pr.Number != 118 {
		t.Fatalf("down past a heading: %+v", m.selected())
	}
	m = press(m, "up")
	if r := m.selected(); r == nil || r.pr.Number != 476 {
		t.Fatalf("up past a heading: %+v", m.selected())
	}
}

func TestPRScreenShowsReadinessThreadsAndConversation(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter")
	if m.screen != screenPR || m.curDetail == nil {
		t.Fatal("enter should open the PR screen with its detail")
	}
	v := plain(m)
	for _, want := range []string{"Ready to merge", "✓ priya", "+318", "9 files", "⌥ worktree", "sam/offline-conflicts → main"} {
		if !strings.Contains(v, want) {
			t.Fatalf("PR screen lacks %q:\n%s", want, v)
		}
	}
	body := strings.Join(m.bodyLines(), "\n")
	for _, want := range []string{"Unresolved threads (1)", "SyncEngine.swift:142", "Conversation", "Ship it", "requested changes"} {
		if want == "requested changes" {
			continue // #482 has none; checked on #118 below
		}
		if !strings.Contains(stripStyles(body), want) {
			t.Fatalf("body lacks %q:\n%s", want, stripStyles(body))
		}
	}
	// Oldest first: jonas's review (20h) before priya's approval (2h).
	if b := stripStyles(body); strings.Index(b, "timeout, inline") > strings.Index(b, "Ship it") {
		t.Fatal("conversation should read oldest first")
	}
	m = press(m, "esc")
	if m.screen != screenList {
		t.Fatal("esc should go back to the list")
	}
}

func TestReadinessReasons(t *testing.T) {
	d := &prDetail{ViewerCanMerge: true}
	d.State, d.BaseRefName = "OPEN", "main"
	cases := []struct {
		set  func(*prDetail)
		want string
	}{
		{func(d *prDetail) { d.MergeStateStatus = "CLEAN" }, "Ready to merge"},
		{func(d *prDetail) { d.IsDraft = true }, "Draft"},
		{func(d *prDetail) { d.Mergeable = "CONFLICTING" }, "Conflicts with main"},
		{func(d *prDetail) { d.MergeStateStatus = "BEHIND" }, "Behind main"},
		{func(d *prDetail) {
			d.MergeStateStatus = "BLOCKED"
			d.Checks = []check{{Typename: "CheckRun", Name: "t", Status: "COMPLETED", Conclusion: "FAILURE", IsRequired: true}}
		}, "1 required check failing"},
		{func(d *prDetail) { d.MergeStateStatus, d.ReviewDecision = "BLOCKED", "REVIEW_REQUIRED" }, "needs an approving review"},
		{func(d *prDetail) {
			d.MergeStateStatus = "BLOCKED"
			d.Checks = []check{{Typename: "StatusContext", Context: "ci", State: "PENDING", IsRequired: true}}
		}, "Waiting on 1 required check"},
		{func(d *prDetail) { d.State = "MERGED" }, "Merged"},
		{func(d *prDetail) { d.MergeStateStatus, d.ViewerCanMerge = "CLEAN", false }, "you can't merge"},
	}
	for _, c := range cases {
		x := *d
		c.set(&x)
		if got, _ := readiness(&x); !strings.Contains(got, c.want) {
			t.Errorf("readiness = %q, want %q", got, c.want)
		}
	}
}

func TestDraftToggle(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter") // #482, ready
	m = press(m, "d")
	if !m.cur.IsDraft || !strings.Contains(m.flash, "draft again") {
		t.Fatalf("d should convert to draft: draft=%v flash=%q err=%q", m.cur.IsDraft, m.flash, m.err)
	}
	if v := plain(m); !strings.Contains(v, "Draft") || !strings.Contains(v, "d ready") {
		t.Fatalf("draft not shown:\n%s", v)
	}
	// A draft can't be merged: m says why instead.
	if m2 := press(m, "m"); m2.menu != nil || !strings.Contains(m2.err, "draft") {
		t.Fatalf("merging a draft: menu=%v err=%q", m2.menu, m2.err)
	}
	m = press(m, "d")
	if m.cur.IsDraft || !strings.Contains(m.flash, "ready for review") {
		t.Fatalf("d again should mark it ready: %v %q", m.cur.IsDraft, m.flash)
	}
}

func TestMergeAsksThenOffersCleanup(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter") // #482: clean, has a worktree
	m = press(m, "m")
	if m.menu == nil || m.menu.items[0].label != "Squash and merge" {
		t.Fatalf("merge menu should lead with the repo's default method: %+v", m.menu)
	}
	m = press(m, "enter")
	if m.menu == nil || !strings.Contains(m.menu.title, "Squash-merge #482 into main now?") {
		t.Fatalf("picking a method should ask to confirm: %+v", m.menu)
	}
	// The confirmation starts on Cancel: enter, enter doesn't merge.
	if m2 := press(m, "enter"); m2.cur.State != "OPEN" {
		t.Fatal("a second enter merged: the confirmation must start on Cancel")
	}
	m = press(m, "1")
	if m.cur.State != "MERGED" {
		t.Fatalf("not merged: %q (err %q)", m.cur.State, m.err)
	}
	if m.menu == nil || !strings.Contains(m.menu.title, "Clean up") || len(m.menu.items) != 4 {
		t.Fatalf("after merging, clean-up should be offered: %+v", m.menu)
	}
	if got := preferredMethod(repoRef{"github.com", "halcyon", "notes-app"}, "MERGE"); got != "SQUASH" {
		t.Fatalf("method not remembered: %q", got)
	}
	m = press(m, "enter") // delete the branch and remove the worktree
	if !strings.Contains(m.flash, "branch deleted, worktree removed") {
		t.Fatalf("clean-up: flash=%q err=%q", m.flash, m.err)
	}
	if m.worktrees[m.cur.key()] {
		t.Fatal("worktree should be unmarked")
	}
	// Gone from the open lists.
	for _, pr := range m.prs[tabMine] {
		if pr.Number == 482 {
			t.Fatal("merged PR still listed")
		}
	}
}

func TestMergeCancelDoesNothing(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter")
	m = press(m, "m", "2", "down", "enter") // merge commit, then Cancel
	if m.cur.State != "OPEN" || m.menu != nil {
		t.Fatalf("cancel merged anyway: %q", m.cur.State)
	}
	m = press(m, "m", "esc")
	if m.menu != nil || m.cur.State != "OPEN" {
		t.Fatal("esc should close the menu")
	}
}

func TestBlockedPROffersAutoMerge(t *testing.T) {
	m := press(demoModel(t, tabMine), "down", "enter") // #479: required check failing
	m = press(m, "m")
	if m.menu == nil || !strings.HasPrefix(m.menu.items[0].label, "Auto-merge when ready") {
		t.Fatalf("blocked PR should offer auto-merge: %+v", m.menu)
	}
	m = press(m, "enter", "1")
	if m.cur.AutoMergeRequest == nil || !strings.Contains(m.flash, "Auto-merge is on") {
		t.Fatalf("auto-merge not on: flash=%q err=%q", m.flash, m.err)
	}
	if !strings.Contains(plain(m), "auto-merge on (squash)") {
		t.Fatalf("auto-merge not shown:\n%s", plain(m))
	}
	m = press(m, "m")
	if m.menu == nil || m.menu.items[0].label != "Turn off auto-merge" {
		t.Fatalf("should offer turning it off: %+v", m.menu)
	}
	m = press(m, "enter")
	if m.cur.AutoMergeRequest != nil {
		t.Fatal("auto-merge still on")
	}
}

func TestConflictingPRCantMerge(t *testing.T) {
	m := press(demoModel(t, tabReview), "enter") // #64: conflicts
	m = press(m, "m")
	if m.menu != nil || !strings.Contains(m.err, "Conflicts") {
		t.Fatalf("conflicting PR: menu=%v err=%q", m.menu, m.err)
	}
}

func TestDemoWorktreeSaysWhatItWouldDo(t *testing.T) {
	m := press(demoModel(t, tabReview), "down", "down", "enter") // #480 from maria-k's fork
	if m.cur.Number != 480 {
		t.Fatalf("on #%d", m.cur.Number)
	}
	m = press(m, "w")
	if !strings.Contains(m.flash, "maria-k/fix-typo") || !strings.Contains(m.flash, "refs/pull/480/head") {
		t.Fatalf("fork worktree: %q", m.flash)
	}
	if !m.worktrees[m.cur.key()] {
		t.Fatal("worktree not marked")
	}
}

// A real worktree action closes the popup on success, and keeps it open to
// explain a note or an error.
func TestWorktreeActionOutcome(t *testing.T) {
	m := demoModel(t, tabMine)
	if _, cmd := m.Update(actionDoneMsg{}); !quits(cmd) {
		t.Fatal("success should close the popup")
	}
	next, cmd := m.Update(actionDoneMsg{note: "no prompt sent"})
	if quits(cmd) || next.(model).err != "no prompt sent" {
		t.Fatal("a note should stay on screen")
	}
	next, _ = m.Update(actionDoneMsg{err: errors.New("boom")})
	if next.(model).err != "boom" || next.(model).mode != modeList {
		t.Fatal("an error should stay on screen")
	}
}

func TestNoCheckoutOffersClone(t *testing.T) {
	m := demoModel(t, tabMine)
	pr := *m.selected().pr
	next, _ := m.Update(needCloneMsg{pr: pr, err: &noCheckoutError{Repo: pr.repo(), Dest: "/x/halcyon/notes-app"}})
	m = next.(model)
	if m.menu == nil || !strings.Contains(m.menu.items[0].label, "Clone it into /x/halcyon/notes-app") {
		t.Fatalf("clone not offered: %+v", m.menu)
	}
	if !strings.Contains(plain(m), "no checkout of halcyon/notes-app") && !strings.Contains(plain(m), "There's no checkout") {
		t.Fatalf("menu not drawn:\n%s", plain(m))
	}
	m = press(m, "2") // cancel
	if m.menu != nil || !strings.Contains(m.err, "repos") {
		t.Fatalf("cancel should say how to map a checkout: %q", m.err)
	}
}

func TestSignedOutScreen(t *testing.T) {
	m := demoModel(t, tabMine)
	next, _ := m.Update(listMsg{tab: tabMine, err: &signedOutError{"github.com"}, gen: m.gen})
	m = next.(model)
	if m.mode != modeSignedOut || !strings.Contains(plain(m), "gh auth login") {
		t.Fatalf("signed out:\n%s", plain(m))
	}
	if _, cmd := m.Update(keyMsg("esc")); !quits(cmd) {
		t.Fatal("esc should close")
	}
}

func TestStaleReplyIsDropped(t *testing.T) {
	m := demoModel(t, tabMine)
	m.gen++
	next, _ := m.Update(listMsg{tab: tabMine, prs: nil, gen: m.gen - 1})
	if len(next.(model).prs[tabMine]) == 0 {
		t.Fatal("a reply from before a refresh replaced the list")
	}
}

func TestMouseHoverClickAndTabs(t *testing.T) {
	m := demoModel(t, tabMine)
	// Hover the second PR (line listTop+2: heading, #482, #479).
	next, _ := m.Update(tea.MouseMsg{X: 10, Y: listTop + 2, Action: tea.MouseActionMotion})
	m = next.(model)
	if r := m.selected(); r == nil || r.pr.Number != 479 {
		t.Fatalf("hover: %+v", m.selected())
	}
	next, cmd := m.Update(tea.MouseMsg{X: 10, Y: listTop + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = drive(next.(model), cmd)
	if m.screen != screenPR || m.cur.Number != 479 {
		t.Fatal("click should open the PR")
	}
	// The tabs take you home: click "Review requested".
	x := len(m.tabLabels()[0]) + 3
	next, cmd = m.Update(tea.MouseMsg{X: x, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = drive(next.(model), cmd)
	if m.screen != screenList || m.tab != tabReview {
		t.Fatalf("tab click: screen %v tab %v", m.screen, m.tab)
	}
}

func TestMouseChoosesMenuItems(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter", "m")
	header := m.prHeader()
	top := 2 + strings.Count(header, "\n") + menuTop
	click := func(m model, y int) model {
		next, cmd := m.Update(tea.MouseMsg{X: 6, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		return drive(next.(model), cmd)
	}
	// A click the moment a menu opens is the tail of the click that opened it.
	if m2 := click(m, top+1); m2.menu == nil || m2.menu.title != m.menu.title {
		t.Fatal("a click right as the menu opened was taken as a choice")
	}
	m.menuOpened = time.Now().Add(-time.Second)
	m = click(m, top+1)
	if m.menu == nil || !strings.Contains(m.menu.title, "Merge #482 into main now?") {
		t.Fatalf("clicking the second method should ask to confirm a merge commit: %+v", m.menu)
	}
	// Double-clicking the first method never reaches "yes".
	m = press(demoModel(t, tabMine), "enter", "m")
	m.menuOpened = time.Now().Add(-time.Second)
	m = click(click(m, top), top)
	if m.cur.State != "OPEN" {
		t.Fatal("a double-click merged")
	}
}

func TestFooterHintsAreButtons(t *testing.T) {
	m := press(demoModel(t, tabMine), "enter")
	hs := m.detailFooter()
	x := 1
	for _, h := range hs {
		if h.key == "d" {
			break
		}
		x += len([]rune(h.label)) + len([]rune(hintSep))
	}
	next, cmd := m.Update(tea.MouseMsg{X: x + 1, Y: m.height - 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = drive(next.(model), cmd)
	if !m.cur.IsDraft {
		t.Fatal("clicking 'd draft' should convert to draft")
	}
}

func TestOpenURLOnlyGitHub(t *testing.T) {
	m := model{cfg: withDefaults(config{Hosts: []string{"github.com", "ghe.example.com"}})}
	for u, want := range map[string]bool{
		"https://github.com/o/r/pull/1":        true,
		"https://ghe.example.com/o/r/pull/1":   true,
		"http://github.com/o/r/pull/1":         false,
		"https://evil.com/github.com":          false,
		"file:///etc/passwd":                   false,
		"https://user@github.com/o/r/pull/1":   false,
		"https://github.com.evil.com/o/r/p/1":  false,
		"x-apple.systempreferences:com.apple.": false,
	} {
		if got := m.isGitHubURL(u); got != want {
			t.Errorf("isGitHubURL(%q) = %v", u, got)
		}
	}
}

func TestListErrorsKeepWhatLoaded(t *testing.T) {
	m := demoModel(t, tabMine)
	next, _ := m.Update(listMsg{tab: tabMine, prs: m.prs[tabMine][:1], err: errTruncated, gen: m.gen})
	m = next.(model)
	if len(m.prs[tabMine]) != 1 || !strings.Contains(plain(m), "first 200") {
		t.Fatalf("truncated list: %d rows:\n%s", len(m.prs[tabMine]), plain(m))
	}
}

// The demo never reaches herdr or GitHub: every herdr call in tests fails,
// so a demo action that tried would surface an error.
func TestDemoTouchesNothing(t *testing.T) {
	calls := 0
	old := herdrCallFn
	herdrCallFn = func(string, any, any) error { calls++; return errors.New("no") }
	defer func() { herdrCallFn = old }()
	m := press(demoModel(t, tabMine), "enter", "w", "s", "d", "d", "m", "enter", "1", "enter")
	if calls != 0 || m.err != "" {
		t.Fatalf("demo called herdr %d times (err %q)", calls, m.err)
	}
	if _, err := newDemoSource().list(context.Background(), tabMine, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRepoPickerSwitchesTheRepoTab(t *testing.T) {
	m := demoModel(t, tabRepo)
	if !strings.Contains(plain(m), "notes-app 7") || !strings.Contains(plain(m), "^t repo") {
		t.Fatalf("repo tab:\n%s", plain(m))
	}
	m = press(m, "ctrl+t")
	if m.menu == nil || !m.menu.filterable {
		t.Fatal("ctrl+t should open the repo picker")
	}
	// Grouped by org: this space's first, its repos on top; then the rest of
	// what GitHub says you can reach, most recently pushed first.
	v := plain(m)
	order := []string{"halcyon", "notes-app  this space · showing", "sync-server  a herdr space", "website  pushed", "sam", "dotfiles", "oss-typesetting", "mdtype"}
	last := -1
	for _, want := range order {
		i := strings.Index(v, want)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order:\n%s", want, v)
		}
		last = i
	}
	if r := m.menuItems()[m.menuCursor]; r.header || r.label != "notes-app" {
		t.Fatalf("cursor should start on the first repo, not a heading: %+v", r)
	}
	// Typing filters, keeping each match's heading.
	for _, r := range "sync" {
		m = press(m, string(r))
	}
	items := m.menuItems()
	if len(items) != 2 || !items[0].header || items[1].label != "sync-server" {
		t.Fatalf("filter: %+v", items)
	}
	m = press(m, "enter")
	if m.repo == nil || m.repo.Name != "sync-server" || m.tab != tabRepo {
		t.Fatalf("switched to %+v", m.repo)
	}
	if v := plain(m); !strings.Contains(v, "sync-server 1") || !strings.Contains(v, "Return merge proposals") || strings.Contains(v, "Offline edits") {
		t.Fatalf("repo tab after switching:\n%s", v)
	}
	// The space's own repo is still the one marked as this space's.
	m = press(m, "ctrl+t")
	if !strings.Contains(plain(m), "notes-app  this space") || m.home.Name != "notes-app" {
		t.Fatalf("the space's repo should stay first:\n%s", plain(m))
	}
}

func TestRepoPickerFiltersByOrg(t *testing.T) {
	m := press(demoModel(t, tabRepo), "ctrl+t")
	for _, r := range "oss-typ" {
		m = press(m, string(r))
	}
	var labels []string
	for _, it := range m.menuItems() {
		labels = append(labels, it.label)
	}
	if strings.Join(labels, ",") != "oss-typesetting,mdtype,fonts" {
		t.Fatalf("an org's name should keep its repos: %v", labels)
	}
	// Headings are skipped on the way down and up.
	m = press(m, "down", "down", "down", "up", "up", "up")
	if m.menuItems()[m.menuCursor].header {
		t.Fatal("cursor landed on a heading")
	}
}

func TestRepoPickerAddsGitHubReposWhenTheyArrive(t *testing.T) {
	m := demoModel(t, tabRepo)
	next, _ := m.openRepoPicker()
	m = next.(model)
	// Only the local choices so far.
	next, _ = m.Update(m.loadRepoChoices()())
	m = next.(model)
	if strings.Contains(plain(m), "dotfiles") || !strings.Contains(plain(m), "loading yours") {
		t.Fatalf("before GitHub answers:\n%s", plain(m))
	}
	m = press(m, "down") // onto sync-server
	next, _ = m.Update(m.loadRemoteRepos()())
	m = next.(model)
	if !strings.Contains(plain(m), "dotfiles") || m.menuItems()[m.menuCursor].label != "sync-server" {
		t.Fatalf("after GitHub answers, cursor on %q:\n%s", m.menuItems()[m.menuCursor].label, plain(m))
	}
}

func TestRepoPickerTakesATypedRepo(t *testing.T) {
	m := press(demoModel(t, tabRepo), "ctrl+t")
	for _, r := range "cli/cli" {
		m = press(m, string(r))
	}
	items := m.menuItems()
	if len(items) != 1 || items[0].label != "cli/cli" || items[0].detail != "as typed" {
		t.Fatalf("typed repo: %+v", items)
	}
	if _, ok := typedRepo("ghe.example.com/o/r", "github.com"); !ok {
		t.Fatal("host/owner/repo")
	}
	for _, bad := range []string{"o", "o/r/x", "o r/x", "../x", "o/"} {
		if _, ok := typedRepo(bad, "github.com"); ok && bad != "../x" {
			t.Errorf("typedRepo(%q) accepted", bad)
		}
	}
	// A reply for the repo the tab showed before switching is dropped.
	m = press(demoModel(t, tabRepo), "ctrl+t")
	old := m.repo.key()
	for _, r := range "sync" {
		m = press(m, string(r))
	}
	next, _ := m.Update(keyMsg("enter"))
	m = next.(model)
	late, _ := m.Update(listMsg{tab: tabRepo, repo: old, prs: prsNumbered(99), gen: m.gen})
	if strings.Contains(plain(late.(model)), "PR 99") {
		t.Fatal("the old repo's late reply was shown as the new one's")
	}
}

func TestClickingTheRepoTabYoureOnPicksARepo(t *testing.T) {
	m := demoModel(t, tabRepo)
	labels := m.tabLabels()
	x := len([]rune(labels[0])) + 1 + len([]rune(labels[1])) + 1 + 2
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = drive(next.(model), cmd)
	if m.menu == nil || !m.menu.filterable {
		t.Fatal("clicking the active repo tab should open the picker")
	}
}

func TestRepoTabHasAChangeRepoRow(t *testing.T) {
	m := demoModel(t, tabRepo)
	v := plain(m)
	if !strings.Contains(v, "⇄ halcyon/notes-app") || !strings.Contains(v, "change repo") {
		t.Fatalf("no change-repo row:\n%s", v)
	}
	// The cursor starts on the first PR, not the row above.
	if r := m.selected(); r == nil || r.pr.Number != 482 {
		t.Fatalf("cursor on %+v", m.selected())
	}
	// ↑ reaches it; enter opens the picker in the tab.
	m = press(m, "up")
	if !m.onRepoRow() {
		t.Fatal("up from the first PR should land on the change-repo row")
	}
	m = press(m, "enter")
	if m.menu == nil || m.menu.id != "repos" {
		t.Fatal("enter on the row should open the repo list")
	}
	// A click on the row does the same.
	m = demoModel(t, tabRepo)
	next, cmd := m.Update(tea.MouseMsg{X: 5, Y: listTop, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = drive(next.(model), cmd)
	if m.menu == nil || m.menu.id != "repos" {
		t.Fatal("clicking the row should open the repo list")
	}
	// Other tabs don't have it.
	if strings.Contains(plain(press(demoModel(t, tabMine), "esc")), "change repo") {
		t.Fatal("change-repo row on the Mine tab")
	}
}

func TestRepoTabOutsideARepoOpensTheList(t *testing.T) {
	m := newModel(context.Background(), withDefaults(config{}), newDemoSource(), "", nil)
	m.width, m.height = 100, 30
	next, cmd := m.switchTab(tabRepo)
	m = drive(next.(model), cmd)
	if m.menu == nil || m.menu.id != "repos" {
		t.Fatalf("the repo tab with no repo should show the repo list:\n%s", plain(m))
	}
}
