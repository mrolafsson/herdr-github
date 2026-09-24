package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
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
