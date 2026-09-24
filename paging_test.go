package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// pagedSource serves a tab in pages: pages[cursor] → the PRs and next cursor.
type pagedSource struct {
	demoSource
	pages map[string]page
	err   error
}

func (s *pagedSource) list(_ context.Context, t tab, cursor string) (page, error) {
	if s.err != nil {
		return page{}, s.err
	}
	if t != tabMine {
		return page{}, nil
	}
	return s.pages[cursor], nil
}

func prsNumbered(ns ...int) []pullRequest {
	var out []pullRequest
	for _, n := range ns {
		pr := pullRequest{Host: "github.com", Number: n, Title: fmt.Sprintf("PR %d", n), State: "OPEN"}
		pr.Repository.NameWithOwner = "o/r"
		out = append(out, pr)
	}
	return out
}

func TestPagesArriveAndKeepTheCursor(t *testing.T) {
	src := &pagedSource{pages: map[string]page{
		"":   {prs: prsNumbered(1, 2, 3), next: "c2"},
		"c2": {prs: prsNumbered(3, 4)}, // #3 again: it moved between pages
	}}
	m := newModel(context.Background(), withDefaults(config{}), src, "", nil)
	m.width, m.height = 100, 30
	m.demo = true // no cache writes
	next, cmd := m.Update(listMsg{tab: tabMine, prs: src.pages[""].prs, next: "c2", gen: m.gen})
	m = next.(model)
	if !m.paging[tabMine] || !strings.Contains(plain(m), "Mine 3+") || !strings.Contains(plain(m), "updating") {
		t.Fatalf("first page should say more is coming:\n%s", plain(m))
	}
	m = press(m, "down") // on #2
	m = drive(m, cmd)
	if m.paging[tabMine] || len(m.prs[tabMine]) != 4 {
		t.Fatalf("after the second page: paging=%v, %d PRs", m.paging[tabMine], len(m.prs[tabMine]))
	}
	if r := m.selected(); r == nil || r.pr.Number != 2 {
		t.Fatalf("cursor moved off #2: %+v", m.selected())
	}
	if v := plain(m); !strings.Contains(v, "Mine 4 ") || strings.Contains(v, "updating") {
		t.Fatalf("done:\n%s", v)
	}
}

func TestCachedListShowsFirstThenIsReplaced(t *testing.T) {
	src := &pagedSource{pages: map[string]page{"": {prs: prsNumbered(7)}}}
	m := newModel(context.Background(), withDefaults(config{}), src, "", nil)
	m.width, m.height = 100, 30
	m = m.useCache(map[tab][]pullRequest{tabMine: prsNumbered(5, 6)})
	if m.mode != modeList || !m.stale[tabMine] || !strings.Contains(plain(m), "PR 5") || !strings.Contains(plain(m), "updating") {
		t.Fatalf("cache not shown at once:\n%s", plain(m))
	}
	m = drive(m, m.loadAll())
	if m.stale[tabMine] || strings.Contains(plain(m), "PR 5") || !strings.Contains(plain(m), "PR 7") {
		t.Fatalf("fresh list didn't replace the cache:\n%s", plain(m))
	}
}

func TestFailedRefreshKeepsTheCachedList(t *testing.T) {
	src := &pagedSource{err: errors.New("GitHub: offline")}
	m := newModel(context.Background(), withDefaults(config{}), src, "", nil)
	m.width, m.height = 100, 30
	m = m.useCache(map[tab][]pullRequest{tabMine: prsNumbered(5)})
	m = drive(m, m.loadAll())
	v := plain(m)
	if !strings.Contains(v, "PR 5") || !strings.Contains(v, "GitHub: offline") {
		t.Fatalf("cached list should stay, with the reason:\n%s", v)
	}
}

func TestListCacheRoundTrip(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	here := &repoRef{"github.com", "o", "r"}
	saveListCache(tabMine, prsNumbered(1, 2), here)
	saveListCache(tabRepo, prsNumbered(3), here)
	got := readListCache(here)
	if len(got[tabMine]) != 2 || len(got[tabRepo]) != 1 || got[tabMine][0].Host != "github.com" {
		t.Fatalf("round trip: %+v", got)
	}
	// Another repo's tab isn't shown as this one's.
	if got := readListCache(&repoRef{"github.com", "o", "other"}); got[tabRepo] != nil || len(got[tabMine]) != 2 {
		t.Fatalf("other repo: %+v", got)
	}
}

func TestFirstPageIsGroupedToo(t *testing.T) {
	prs := prsNumbered(1, 2)
	prs[0].IsDraft = true // GitHub's order: the draft was updated last
	m := newModel(context.Background(), withDefaults(config{}), &pagedSource{}, "", &repoRef{"github.com", "o", "r"})
	m.width, m.height, m.tab = 100, 30, tabRepo
	next, _ := m.Update(listMsg{tab: tabRepo, repo: "github.com/o/r", prs: prs, gen: m.gen})
	v := plain(next.(model))
	if strings.Index(v, "Drafts") < strings.Index(v, "Open") {
		t.Fatalf("drafts before open on the first page:\n%s", v)
	}
}

func TestRepoListCacheRoundTripKeepsLists(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	here := &repoRef{"github.com", "o", "r"}
	saveListCache(tabMine, prsNumbered(1), here)
	saveRepoCache([]repoInfo{{repoRef{"github.com", "acme", "app"}, nowFn()}, {repoRef{"ghe.old.com", "x", "y"}, nowFn()}})
	got := readRepoCache(withDefaults(config{}))
	if len(got) != 1 || got[0].Repo.Name != "app" {
		t.Fatalf("repos (only configured hosts): %+v", got)
	}
	if len(readListCache(here)[tabMine]) != 1 {
		t.Fatal("saving repos lost the lists")
	}
}

// With last time's repos in hand, the list shows them at once and a failed
// refresh keeps them.
func TestRepoListShowsCachedReposAtOnce(t *testing.T) {
	src := &pagedSource{}
	m := newModel(context.Background(), withDefaults(config{}), src, "", &repoRef{"github.com", "o", "r"})
	m.width, m.height = 100, 30
	m.repoRemote = []repoInfo{{repoRef{"github.com", "acme", "app"}, nowFn()}}
	m.remoteLoading = true
	next, cmd := m.openRepoPicker()
	m = drive(next.(model), cmd)
	if v := plain(m); !strings.Contains(v, "acme") || strings.Contains(v, "loading yours") {
		t.Fatalf("cached repos not shown at once:\n%s", v)
	}
	next, _ = m.Update(remoteReposMsg{err: errors.New("offline")})
	if v := plain(next.(model)); !strings.Contains(v, "acme") {
		t.Fatalf("a failed refresh dropped the cached repos:\n%s", v)
	}
}

// pagedRepos serves the repo list in two pages.
type pagedRepos struct{ pagedSource }

func (s *pagedRepos) repos(_ context.Context, cursor string) ([]repoInfo, string, error) {
	r := func(n string) repoInfo { return repoInfo{repoRef{"github.com", "acme", n}, nowFn()} }
	if cursor == "" {
		return []repoInfo{r("one")}, "p2", nil
	}
	return []repoInfo{r("two")}, "", nil
}

func TestRepoListArrivesPageByPage(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	m := newModel(context.Background(), withDefaults(config{}), &pagedRepos{}, "", &repoRef{"github.com", "o", "r"})
	m.width, m.height = 100, 30
	m.repoRemote = []repoInfo{{repoRef{"github.com", "acme", "old"}, nowFn()}} // last time's
	// First page: shown at once, last time's still filling the gap.
	next, cmd := m.Update(m.loadRemoteRepos("")())
	m = next.(model)
	if len(m.repoRemote) != 2 || m.remoteLoaded {
		t.Fatalf("after page 1: %+v loaded=%v", m.repoRemote, m.remoteLoaded)
	}
	m = drive(m, cmd)
	if len(m.repoRemote) != 2 || m.repoRemote[1].Repo.Name != "two" || !m.remoteLoaded {
		t.Fatalf("after page 2 (old one gone): %+v", m.repoRemote)
	}
	if got := readRepoCache(withDefaults(config{})); len(got) != 2 || got[1].Repo.Name != "two" {
		t.Fatalf("not saved: %+v", got)
	}
}

// Last time's lists are laid out before the popup knows its size; once it
// does, the list starts at the top, change-repo row and all.
func TestCachedListIsLaidOutAgainOnceSized(t *testing.T) {
	here := &repoRef{"github.com", "o", "r"}
	m := newModel(context.Background(), withDefaults(config{}), &pagedSource{}, "", here)
	m.tab = tabRepo
	m = m.useCache(map[tab][]pullRequest{tabRepo: prsNumbered(1, 2)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	if m.offset != 0 || !strings.Contains(plain(m), "⇄ o/r") {
		t.Fatalf("offset %d:\n%s", m.offset, plain(m))
	}
}
