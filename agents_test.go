package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// createdLine is a transcript line as Claude Code writes it for a command
// that created a PR.
func createdLine(url string, number int, action string, at time.Time) string {
	data, _ := json.Marshal(map[string]any{
		"type": "user", "timestamp": at.Format(time.RFC3339Nano), "cwd": "/work/it's here",
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "content": url}}},
		"toolUseResult": map[string]any{"stdout": url, "gitOperation": map[string]any{"pr": map[string]any{"number": number, "url": url, "action": action}}},
	})
	return string(data) + "\n"
}

func linkLine(url string, number int, at time.Time) string {
	data, _ := json.Marshal(map[string]any{"type": "pr-link", "sessionId": "s", "prNumber": number, "prUrl": url, "timestamp": at.Format(time.RFC3339Nano)})
	return string(data) + "\n"
}

func titleLine(title string) string {
	data, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": title})
	return string(data) + "\n"
}

func TestParseLine(t *testing.T) {
	cfg := withDefaults(config{})
	at := nowFn()
	n, ok := parseLine(cfg, []byte(createdLine("https://github.com/Acme/App/pull/12", 12, "created", at)))
	if !ok || openedKey(n.repo, n.number) != "github.com/acme/app#12" || n.repo.Owner != "Acme" || !n.opened || !n.at.Equal(at) {
		t.Fatalf("created: %+v %v", n, ok)
	}
	if n, ok = parseLine(cfg, []byte(linkLine("https://github.com/acme/app/pull/12", 12, at))); !ok || n.number != 12 || n.opened || !n.at.Equal(at) {
		t.Fatalf("linked: %+v %v", n, ok)
	}
	if n, ok = parseLine(cfg, []byte(titleLine("Fix \x1b]0;x\x07the sync"))); !ok || n.title != "Fix the sync" || n.number != 0 {
		t.Fatalf("title: %+v %v", n, ok)
	}
	if got := lineCwd([]byte(createdLine("https://github.com/acme/app/pull/12", 12, "created", at))); got != "/work/it's here" {
		t.Fatalf("cwd: %q", got)
	}
	quoted, _ := json.Marshal(map[string]any{"toolUseResult": map[string]any{"stdout": createdLine("https://github.com/acme/app/pull/12", 12, "created", at) + linkLine("https://github.com/acme/app/pull/12", 12, at)}})
	for name, line := range map[string]string{
		"a comment, not a new PR":   createdLine("https://github.com/acme/app/pull/12", 12, "commented", at),
		"a host that isn't ours":    createdLine("https://evil.example/acme/app/pull/12", 12, "created", at),
		"a link to another host":    linkLine("https://evil.example/acme/app/pull/12", 12, at),
		"not https":                 createdLine("http://github.com/acme/app/pull/12", 12, "created", at),
		"a number the URL doesn't":  linkLine("https://github.com/acme/app/pull/12", 13, at),
		"not a PR's URL":            createdLine("https://github.com/acme/app/issues/12", 12, "created", at),
		"output that quotes a line": string(quoted),
		"a result that's a string":  `{"toolUseResult":"\"gitOperation\" \"created\""}`,
		"not JSON":                  `"gitOperation" "created" "type":"pr-link"`,
		"an empty title":            titleLine(" "),
	} {
		if n, ok := parseLine(cfg, []byte(line)); ok {
			t.Errorf("%s: took %+v", name, n)
		}
	}
}

func TestScanLinesReadsOnlyWhatIsNew(t *testing.T) {
	cfg := withDefaults(config{})
	path := filepath.Join(t.TempDir(), "s.jsonl")
	at := nowFn()
	one := createdLine("https://github.com/acme/app/pull/1", 1, "created", at)
	two := createdLine("https://github.com/acme/app/pull/2", 2, "created", at)
	long := `{"type":"user","message":"` + strings.Repeat("x", 200_000) + `"}` + "\n"
	scan := func(from int64) (int64, []int) {
		var got []int
		next := scanLines(path, from, func(line []byte) {
			if n, ok := parseLine(cfg, line); ok {
				got = append(got, n.number)
			}
		})
		return next, got
	}
	// The line still being written is left for next time.
	os.WriteFile(path, []byte(long+one+two[:40]), 0o600)
	next, got := scan(0)
	if fmt.Sprint(got) != "[1]" || next != int64(len(long)+len(one)) {
		t.Fatalf("got %v, next %d (want %d)", got, next, len(long)+len(one))
	}
	os.WriteFile(path, []byte(long+one+two), 0o600)
	if next, got = scan(next); fmt.Sprint(got) != "[2]" || next != int64(len(long)+len(one)+len(two)) {
		t.Fatalf("second read: %v, next %d", got, next)
	}
	if _, got = scan(next); len(got) != 0 {
		t.Fatalf("nothing new, but got %v", got)
	}
	// A file shorter than where we were has been rewritten: read it all.
	os.WriteFile(path, []byte(two), 0o600)
	if next, got = scan(next); fmt.Sprint(got) != "[2]" || next != int64(len(two)) {
		t.Fatalf("after a rewrite: %v, next %d", got, next)
	}
	if next := scanLines(filepath.Join(t.TempDir(), "none"), 7, nil); next != 7 {
		t.Fatalf("a missing file moved the offset to %d", next)
	}
}

// Transcripts are gone through by when they were written: the first sweep
// reaches back a month, later ones read what's been written since.
func TestSweepReadsRecentTranscripts(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cfg := withDefaults(config{})
	now := nowFn()
	url := func(n int) string { return fmt.Sprintf("https://github.com/o/r/pull/%d", n) }
	touch := func(session string, at time.Time) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(claudeDir(), "projects", "-work", session+".jsonl"), at, at); err != nil {
			t.Fatal(err)
		}
	}
	claudeSession(t, "session-old1", "", linkLine(url(1), 1, now))
	touch("session-old1", now.Add(-40*24*time.Hour))
	claudeSession(t, "session-week", "", titleLine("Last week's work"), linkLine(url(2), 2, now))
	touch("session-week", now.Add(-7*24*time.Hour))
	claudeSession(t, "session-live", "", linkLine(url(3), 3, now))
	touch("session-live", now.Add(-time.Minute))
	claudeSession(t, "../../escape", "", linkLine(url(4), 4, now))

	st := readLabelState()
	st.trackSessions(cfg, nil, now)
	if _, ok := st.PRs["github.com/o/r#1"]; ok || len(st.PRs) != 2 {
		t.Fatalf("the first sweep should take the last month's: %v", sortedKeys(st.PRs))
	}
	if ss := st.Sessions["session-week"]; ss.Title != "Last week's work" || ss.Pane != "" {
		t.Fatalf("a session with no pane: %+v", ss)
	}
	if len(st.Scanned) != 1 {
		t.Fatalf("only a transcript still being written keeps its place: %v", st.Scanned)
	}
	// Written to again: the quiet one is read from its start, the live one
	// from where it was. Nothing is counted twice either way.
	claudeSession(t, "session-week", "", linkLine(url(5), 5, now))
	claudeSession(t, "session-live", "", linkLine(url(6), 6, now))
	touch("session-week", now.Add(time.Minute))
	touch("session-live", now.Add(time.Minute))
	st.trackSessions(cfg, nil, now.Add(2*time.Minute))
	if got := fmt.Sprint(sortedKeys(st.PRs)); got != "[github.com/o/r#2 github.com/o/r#3 github.com/o/r#5 github.com/o/r#6]" {
		t.Fatalf("after more was written: %s", got)
	}
}

func TestPRsToken(t *testing.T) {
	passed := rollupOf([]check{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}})
	pr := func(n int, checks string) trackedPR {
		s := prStatus{Number: n, State: "OPEN"}
		if checks != "" {
			s.Commits = rollupOf([]check{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: checks}})
		}
		return trackedPR{Host: "github.com", Owner: "o", Name: "r", Number: n, Status: &s}
	}
	if got := prsToken(nil, "", nil); got != "" {
		t.Fatalf("no PRs: %q", got)
	}
	if got := prsToken(nil, "", []trackedPR{pr(7, "SUCCESS"), pr(9, ""), pr(8, "FAILURE")}); got != "#9 #8 ✗ #7 ✓" {
		t.Fatalf("newest first, with checks: %q", got)
	}
	if got := prsToken(nil, "", []trackedPR{pr(1, ""), pr(2, ""), pr(3, ""), pr(4, ""), pr(5, "")}); got != "#5 #4 #3 +2" {
		t.Fatalf("the rest counted: %q", got)
	}
	// The branch's PR comes first, with its status, and isn't said twice
	// for having been opened here too.
	branch := &prStatus{Number: 8, State: "OPEN", IsDraft: true, Commits: passed}
	if got := prsToken(branch, "github.com/o/r#8", []trackedPR{pr(8, "SUCCESS"), pr(9, "FAILURE"), pr(7, "")}); got != "#8 draft ✓ #9 ✗ #7" {
		t.Fatalf("with the branch's: %q", got)
	}
	// Another repo's #8 is another PR; a merged branch PR is still the branch's.
	merged := &prStatus{Number: 8, State: "MERGED"}
	if got := prsToken(merged, "github.com/o/other#8", []trackedPR{pr(8, "")}); got != "#8 merged #8" {
		t.Fatalf("the same number elsewhere: %q", got)
	}
	if got := prsToken(branch, "github.com/o/r#8", []trackedPR{pr(1, ""), pr(2, ""), pr(3, "")}); got != "#8 draft ✓ #3 #2 +1" {
		t.Fatalf("counted with the branch's: %q", got)
	}
}

// claudeSession writes a session's transcript where Claude Code would.
func claudeSession(t *testing.T, session, sub string, lines ...string) {
	t.Helper()
	dir := filepath.Join(claudeDir(), "projects", "-work")
	path := filepath.Join(dir, session+".jsonl")
	if sub != "" {
		path = filepath.Join(dir, session, "subagents", sub+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(strings.Join(lines, ""))
}

func claudePane(id, ws, session string, tokens map[string]string) paneInfo {
	return paneInfo{PaneID: id, WorkspaceID: ws, TerminalID: "term-" + id, TerminalTitle: "agent " + id, Agent: "claude",
		AgentSession: &agentSession{Kind: "id", Value: session}, Tokens: tokens}
}

func TestTickTracksThePRsAgentsOpened(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	h := &fakeHerdr{}
	h.install(t)
	old := listWorkspacesFn
	listWorkspacesFn = listWorkspaces
	t.Cleanup(func() { listWorkspacesFn = old })

	start := nowFn()
	oldNow := nowFn
	t.Cleanup(func() { nowFn = oldNow })
	at := func(d time.Duration) { nowFn = func() time.Time { return start.Add(d) } }
	url := func(n int) string { return fmt.Sprintf("https://github.com/o/r/pull/%d", n) }

	// One agent opened two PRs (one through a subagent) from a space that
	// isn't a checkout at all; another agent only ran something that looked
	// like opening the first, later; and a session in no pane looked at it.
	claudeSession(t, "session-aaaa", "", createdLine(url(7), 7, "created", start.Add(-time.Hour)), linkLine(url(7), 7, start.Add(-time.Hour)))
	claudeSession(t, "session-dddd", "", createdLine("https://x.example/a/b/pull/1", 1, "commented", start), titleLine("Why is CI red"), linkLine(url(7), 7, start.Add(-2*time.Hour)))
	claudeSession(t, "session-aaaa", "agent-x", createdLine(url(9), 9, "created", start.Add(-time.Minute)))
	claudeSession(t, "session-bbbb", "", createdLine(url(7), 7, "created", start.Add(-time.Second)))
	h.workspaces = []workspaceInfo{{WorkspaceID: "w1"}, {WorkspaceID: "w2"}}
	h.panes = []paneInfo{claudePane("p1", "w1", "session-aaaa", nil), claudePane("p2", "w2", "session-bbbb", nil), {PaneID: "p3", WorkspaceID: "w1"}}

	states := map[int]string{7: "OPEN", 9: "OPEN"}
	var asked [][]int
	oldLookup := numberLookupFn
	t.Cleanup(func() { numberLookupFn = oldLookup })
	numberLookupFn = func(_ context.Context, r repoRef, numbers []int) (map[int]prStatus, error) {
		if r.key() != "github.com/o/r" {
			t.Errorf("asked about %v", r)
		}
		asked = append(asked, numbers)
		out := map[int]prStatus{}
		for _, n := range numbers {
			if s, ok := states[n]; ok {
				out[n] = prStatus{Number: n, State: s, Commits: rollupOf([]check{{Typename: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}})}
			}
		}
		return out, nil
	}
	cfg := withDefaults(config{})
	prs := func(reports []map[string]any, key, id string) any {
		r := reportFor(reports, key, id)
		if r == nil {
			return "(no report)"
		}
		return r["tokens"].(map[string]any)["prs"]
	}
	shows := func(reports []map[string]any) { // herdr shows what it was told
		for i, p := range h.panes {
			if r := reportFor(reports, "pane_id", p.PaneID); r != nil {
				h.panes[i].Tokens = map[string]string{}
				if v, ok := r["tokens"].(map[string]any)["prs"].(string); ok {
					h.panes[i].Tokens["prs"] = v
				}
			}
		}
		for i, w := range h.workspaces {
			if r := reportFor(reports, "workspace_id", w.WorkspaceID); r != nil {
				h.workspaces[i].Tokens = map[string]string{}
				if v, ok := r["tokens"].(map[string]any)["prs"].(string); ok {
					h.workspaces[i].Tokens["prs"] = v
				}
			}
		}
	}

	if err := tick(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}
	reports := h.takeReports()
	if len(asked) != 1 || len(asked[0]) != 2 {
		t.Fatalf("one query for both PRs, got %v", asked)
	}
	if got := prs(reports, "pane_id", "p1"); got != "#9 ✓ #7 ✓" {
		t.Fatalf("the agent's PRs: %v", got)
	}
	if got := prs(reports, "workspace_id", "w1"); got != "#9 ✓ #7 ✓" {
		t.Fatalf("its space's: %v", got)
	}
	if reportFor(reports, "pane_id", "p2") != nil || reportFor(reports, "pane_id", "p3") != nil {
		t.Fatalf("the later claim, or a shell, got a PR: %v", reports)
	}
	shows(reports)
	agents := func(n int) string {
		t.Helper()
		var out []string
		for _, a := range readAgents()[fmt.Sprintf("github.com/o/r#%d", n)] {
			out = append(out, fmt.Sprintf("%s@%s %s", a.Name, a.Pane, a.Part))
		}
		return strings.Join(out, ", ")
	}
	// Whoever opened it, then who's in a pane, then the rest.
	if got := agents(7); got != "agent p1@p1 opened it, agent p2@p2 worked on it, Why is CI red@ worked on it" {
		t.Fatalf("the popup's view: %s", got)
	}
	if as := readAgents()["github.com/o/r#7"]; as[2].resume() != `cd '/work/it'\''s here' && claude --resume session-dddd` {
		t.Fatalf("resuming a session with no pane: %s", as[2].resume())
	}

	// /clear gives the pane a new session: its PRs stay its own. A new PR
	// from the new session joins them.
	h.panes[0].AgentSession.Value = "session-cccc"
	claudeSession(t, "session-cccc", "", createdLine(url(11), 11, "created", start.Add(time.Minute)))
	states[11] = "OPEN"
	at(2 * time.Minute)
	if err := tick(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}
	reports = h.takeReports()
	if got := prs(reports, "pane_id", "p1"); got != "#11 ✓ #9 ✓ #7 ✓" {
		t.Fatalf("after /clear: %v", got)
	}
	shows(reports)
	if got := agents(7); !strings.HasPrefix(got, "agent p1@p1 opened it, agent p2@p2") {
		t.Fatalf("one line for a pane, whatever its session: %s", got)
	}

	// Merged: no longer the agent's to watch.
	states[9], states[11] = "MERGED", "CLOSED"
	at(4 * time.Minute)
	_ = tick(context.Background(), cfg, false)
	reports = h.takeReports()
	if got := prs(reports, "pane_id", "p1"); got != "#7 ✓" {
		t.Fatalf("after a merge: %v", got)
	}
	shows(reports)
	if got := agents(9); got != "" {
		t.Fatalf("a merged PR is still remembered: %s", got)
	}

	// The pane closes: the PR is still its session's, with nowhere to go.
	h.panes = h.panes[1:]
	at(6 * time.Minute)
	_ = tick(context.Background(), cfg, false)
	reports = h.takeReports()
	if got := prs(reports, "workspace_id", "w1"); got != nil {
		t.Fatalf("the space still shows a gone agent's PR: %v", got)
	}
	if got := agents(7); !strings.HasPrefix(got, "agent p1@ opened it, agent p2@p2 worked on it") {
		t.Fatalf("a gone agent: %s", got)
	}
	// Its session comes back in another pane: so does the PR.
	h.panes = append(h.panes, claudePane("p9", "w2", "session-aaaa", nil))
	at(8 * time.Minute)
	_ = tick(context.Background(), cfg, false)
	if got := prs(h.takeReports(), "pane_id", "p9"); got != "#7 ✓" {
		t.Fatalf("resumed elsewhere: %v", got)
	}

	// Turned off: forgotten, and cleared.
	off := false
	cfg.AgentPRs = &off
	h.panes[len(h.panes)-1].Tokens = map[string]string{"prs": "#7 ✓"}
	at(10 * time.Minute)
	_ = tick(context.Background(), cfg, false)
	if got := prs(h.takeReports(), "pane_id", "p9"); got != nil {
		t.Fatalf("off, but still shown: %v", got)
	}
	if len(readAgents()) != 0 {
		t.Fatal("off, but still remembered")
	}
}

func TestTickKeepsOpenedPRsWhenGitHubFails(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	h := &fakeHerdr{}
	h.install(t)
	old := listWorkspacesFn
	listWorkspacesFn = listWorkspaces
	t.Cleanup(func() { listWorkspacesFn = old })
	claudeSession(t, "session-aaaa", "", createdLine("https://github.com/o/r/pull/7", 7, "created", nowFn()))
	h.workspaces = []workspaceInfo{{WorkspaceID: "w1"}}
	h.panes = []paneInfo{claudePane("p1", "w1", "session-aaaa", nil)}
	oldLookup := numberLookupFn
	t.Cleanup(func() { numberLookupFn = oldLookup })
	numberLookupFn = func(context.Context, repoRef, []int) (map[int]prStatus, error) { return nil, errSignedOut }
	if err := tick(context.Background(), withDefaults(config{}), true); err == nil {
		t.Fatal("the failure wasn't reported")
	}
	if len(h.takeReports()) != 0 {
		t.Fatal("a PR GitHub hasn't vouched for was shown")
	}
	if as := readAgents()["github.com/o/r#7"]; len(as) != 1 || as[0].Pane != "p1" {
		t.Fatalf("forgotten because GitHub was unreachable: %+v", as)
	}
}

func TestPRsByNumberLeavesOutWhatIsNotThere(t *testing.T) {
	var errs []map[string]any
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeReq(t, r)
		if req.Variables["n0"] != float64(7) || req.Variables["n1"] != float64(8) || !strings.Contains(req.Query, "n1: pullRequest(number: $n1)") {
			t.Errorf("query: %s %v", req.Query, req.Variables)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data":   map[string]any{"repository": map[string]any{"n0": map[string]any{"number": 7, "state": "OPEN"}, "n1": nil}},
			"errors": errs,
		})
	})
	c := newClient("github.com")
	errs = []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a PullRequest with the number of 8."}}
	got, err := c.prsByNumber(context.Background(), repoRef{"github.com", "o", "r"}, []int{7, 8})
	if err != nil || len(got) != 1 || got[7].State != "OPEN" {
		t.Fatalf("got %v, %v", got, err)
	}
	// Anything else missing is GitHub not answering, not the PR being gone.
	errs = []map[string]any{{"type": "SERVICE_UNAVAILABLE", "message": "timeout"}}
	if got, err := c.prsByNumber(context.Background(), repoRef{"github.com", "o", "r"}, []int{7, 8}); err == nil {
		t.Fatalf("a partial answer passed for a whole one: %v", got)
	}
}

// An agent of any kind in a space on a PR's branch is one of its agents.
func TestAgentsOnThePRsBranch(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	h := &fakeHerdr{}
	h.install(t)
	h.panes = []paneInfo{
		{PaneID: "p1", WorkspaceID: "w1", Agent: "codex", AgentStatus: "working", TerminalTitle: "codex"},
		{PaneID: "p2", WorkspaceID: "w1"}, // a shell
		claudePane("p3", "w1", "session-aaaa", nil),
	}
	os.MkdirAll(stateDir(), 0o700)
	st := readLabelState()
	branch := "github.com/o/r\x00o:feat"
	st.Branches[branch] = branchEntry{PR: &prStatus{Number: 12, State: "OPEN"}}
	for _, id := range []string{"w1", "p1", "p2", "p3"} {
		st.Reported[id] = reported{Branch: branch}
	}
	// The Claude one opened it too: it's listed once, as the opener.
	st.PRs["github.com/o/r#12"] = trackedPR{Host: "github.com", Owner: "o", Name: "r", Number: 12, By: map[string]prPart{"session-aaaa": {Opened: nowFn(), Last: nowFn()}}}
	st.Sessions["session-aaaa"] = sessionSeen{Pane: "p3", Terminal: "term-p3"}
	if err := writeLabelState(st); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range readAgents()["github.com/o/r#12"] {
		got = append(got, a.Name+" "+a.Part+" "+a.where())
	}
	if fmt.Sprint(got) != "[agent p3 opened it  codex on its branch working]" {
		t.Fatalf("got %q", got)
	}
}

func TestPopupGoesToAPRsAgents(t *testing.T) {
	m := demoModel(t, tabMine)
	m.demo = false
	// An agent's name finds its PR, though no word of it is in the PR.
	f := press(m, "t", "h", "r", "e", "e", "-", "w", "a", "y")
	if r := f.selected(); r == nil || r.pr.Number != 482 || len(f.rows()) != 2 {
		t.Fatalf("filtering by agent: %+v", f.rows())
	}
	list, screen := ansi.Strip(m.View()), ansi.Strip(press(m, "enter").View())
	if !strings.Contains(list, "Offline sync conflicts +1") || !strings.Contains(list, "^a agents") {
		t.Fatalf("the list doesn't name the agents:\n%s", list)
	}
	for _, want := range []string{"Agents", "Offline sync conflicts", "opened it", "Review the three-way merge", "working", "on its branch", "a agents"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the PR screen lacks %q:\n%s", want, screen)
		}
	}

	var calls []string
	old, oldGuard := herdrCallFn, menuClickGuard
	menuClickGuard = 0
	t.Cleanup(func() { herdrCallFn, menuClickGuard = old, oldGuard })
	herdrCallFn = func(method string, params any, _ any) error {
		calls = append(calls, fmt.Sprint(method, " ", params))
		return nil
	}
	// went: the work a key or click started, done, closes the popup.
	went := func(next tea.Model, cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, cmd = next.(model).Update(cmd())
		return quits(cmd)
	}
	click := func(m model, x, y int) (tea.Model, tea.Cmd) {
		next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion})
		return next.(model).Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	}

	// With more than one, the key asks which; the choice goes there.
	menu := press(m, "ctrl+a")
	if menu.menu == nil || len(menu.menu.items) != 2 || len(calls) != 0 {
		t.Fatalf("no menu of agents: %+v", menu.menu)
	}
	if !went(menu.Update(keyMsg("2"))) || fmt.Sprint(calls) != "[agent.focus map[target:w1:p2]]" {
		t.Fatalf("didn't go to the second agent and close: %v", calls)
	}
	// On the PR screen each agent's line is a button.
	calls = nil
	pr := press(m, "enter")
	top := 2 + strings.Count(pr.prHeaderTop(), "\n")
	if !went(click(pr, 20, top+1)) || fmt.Sprint(calls) != "[agent.focus map[target:w1:p2]]" {
		t.Fatalf("a click on the second agent's line: %v", calls)
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	hover, _ := pr.Update(tea.MouseMsg{X: 20, Y: top, Action: tea.MouseActionMotion})
	lit := hover.(model).View() != pr.View()
	lipgloss.SetColorProfile(termenv.ANSI256)
	if !lit {
		t.Fatal("hovering an agent's line doesn't show it's a button")
	}
	// In the list, a click on the agent goes to the agents; elsewhere on the
	// row, to the PR.
	from, to, ok := m.agentSpan(m.rows()[1].pr)
	if !ok || to <= from {
		t.Fatalf("no span for the row's agent: %d %d", from, to)
	}
	if next, _ := click(m, from, listTop+1); next.(model).menu == nil || next.(model).screen != screenList {
		t.Fatal("a click on a row's agents didn't list them")
	}
	if next, _ := click(m, from-3, listTop+1); next.(model).screen != screenPR {
		t.Fatal("a click beside the agent didn't open the PR")
	}

	// A pane its agent has left is still gone to.
	calls = nil
	herdrCallFn = func(method string, params any, _ any) error {
		calls = append(calls, method)
		if method == "agent.focus" {
			return &herdrError{"not_found", "no agent"}
		}
		return nil
	}
	if !went(pr.goTo(pr.agentsOf(*pr.cur)[0])) || fmt.Sprint(calls) != "[agent.focus pane.focus]" {
		t.Fatalf("no fallback to the pane: %v", calls)
	}

	// An agent with no pane can only be resumed: the command is copied.
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
	gone := press(m, "down", "down") // #476
	var after model
	out := stdout(t, func() { after = press(gone, "ctrl+a") })
	if !strings.Contains(out, "\x1b]52;c;") || !strings.Contains(after.flash, "resumes CRDT spike") {
		t.Fatalf("out %q, flash %q, err %q", out, after.flash, after.err)
	}
	// And a PR no agent here has touched says so.
	if none := press(m, "down", "ctrl+g"); !strings.Contains(none.err, "No agent here has worked on #479") {
		t.Fatalf("err %q", none.err)
	}
}

func TestPanePRs(t *testing.T) {
	now := nowFn()
	open := &prStatus{State: "OPEN"}
	st := readLabelState()
	st.Branches["github.com/o/r\x00o:feat"] = branchEntry{PR: &prStatus{Number: 12, State: "MERGED"}}
	st.Reported["w1"] = reported{Branch: "github.com/o/r\x00o:feat"}
	st.Reported["p1"] = reported{Branch: "github.com/o/r\x00o:feat"}
	st.Sessions["session-aaaa"] = sessionSeen{Pane: "p1"}
	st.Sessions["session-bbbb"] = sessionSeen{Pane: "p2"}
	track := func(n int, status *prStatus, by map[string]prPart) {
		st.PRs[fmt.Sprintf("github.com/o/r#%d", n)] = trackedPR{Host: "github.com", Owner: "o", Name: "r", Number: n, Status: status, By: by}
	}
	track(12, open, map[string]prPart{"session-aaaa": {Opened: now, Last: now}})                              // its branch's too
	track(20, open, map[string]prPart{"session-aaaa": {Opened: now, Last: now}})                              // opened
	track(21, open, map[string]prPart{"session-aaaa": {Opened: now, Last: now}})                              // opened, newer
	track(30, open, map[string]prPart{"session-aaaa": {Last: now}, "session-bbbb": {Opened: now, Last: now}}) // only worked on
	track(31, nil, map[string]prPart{"session-aaaa": {Opened: now, Last: now}})                               // GitHub hasn't vouched for it
	if got := fmt.Sprint(panePRs(st, "p1", "w1")); got != "[github.com/o/r#12 github.com/o/r#21 github.com/o/r#20 github.com/o/r#30]" {
		t.Fatalf("the agent's: %s", got)
	}
	// A pane that isn't an agent's has its space's branch to go by.
	if got := fmt.Sprint(panePRs(st, "p9", "w1")); got != "[github.com/o/r#12]" {
		t.Fatalf("a shell's: %s", got)
	}
	if got := panePRs(st, "p9", "w9"); len(got) != 0 {
		t.Fatalf("nothing to go by: %v", got)
	}
	refs := prRefsOf([]string{"github.com/o/r#12", "github.com/o/r#x", "evil#1", "github.com/o/r#0", "github.com/a/b#7"})
	if len(refs) != 2 || refs[0].key() != "github.com/o/r#12" || refs[1].number != 7 {
		t.Fatalf("refs: %+v", refs)
	}
}

func TestPickerOpensStraightOnAnAgentsPR(t *testing.T) {
	ref := func(repo string, n int) prRef { return prRef{repoRef{"github.com", "halcyon", repo}, n} }
	// In a list from last time: its screen at once, and its keys work
	// though the lists behind it are still loading. You came for the PR, so
	// esc closes the popup; the lists are a ← away.
	m := demoModel(t, tabMine)
	m.mode = modeLoading
	next, cmd := m.openOn([]prRef{ref("notes-app", 479)})
	if next.screen != screenPR || next.cur.Number != 479 || next.opening != "" || cmd == nil {
		t.Fatalf("not on #479's screen: screen %v opening %q", next.screen, next.opening)
	}
	next = drive(next, cmd)
	if view := ansi.Strip(next.View()); !strings.Contains(view, "esc close") || !strings.Contains(view, "← lists") {
		t.Fatalf("the footer doesn't say how to leave:\n%s", next.View())
	}
	if _, cmd := next.Update(keyMsg("esc")); !quits(cmd) {
		t.Fatal("esc on the PR you came for didn't close the popup")
	}
	lists := press(next, "left")
	if lists.screen != screenList {
		t.Fatal("← didn't go to the lists")
	}
	// From the lists it's the picker as ever: esc on a PR goes back to them.
	if back := press(lists, "enter", "esc"); back.screen != screenList {
		t.Fatal("esc on a PR opened from the list left the popup")
	}

	// In no list: asked for first, with no list shown meanwhile.
	m = demoModel(t, tabMine)
	listed := m.View()
	m.prs = map[tab][]pullRequest{}
	next, cmd = m.openOn([]prRef{ref("notes-app", 482)})
	if view := next.View(); !strings.Contains(view, "Opening #482") || strings.Contains(view, "halcyon/sync-server") || !strings.Contains(listed, "halcyon/sync-server") {
		t.Fatalf("the lists show on the way to the PR:\n%s", view)
	}
	if _, cmd := next.Update(keyMsg("esc")); !quits(cmd) {
		t.Fatal("esc while it's opening didn't close the popup")
	}
	if held := press(next, "down", "enter"); held.screen != screenList || held.opening == "" {
		t.Fatal("keys reached the lists behind")
	}
	if next = drive(next, cmd); next.screen != screenPR || next.cur.Number != 482 || next.curDetail == nil || next.opening != "" || !next.direct {
		t.Fatalf("didn't arrive on #482: screen %v opening %q err %q", next.screen, next.opening, next.err)
	}

	// Several: which one? And one that's gone is said, not shown.
	m = demoModel(t, tabMine)
	next, cmd = m.openOn([]prRef{ref("notes-app", 482), ref("sync-server", 118), ref("notes-app", 9999)})
	next = drive(next, cmd)
	if next.menu == nil || len(next.menu.items) != 2 || !strings.Contains(next.err, "9999") {
		t.Fatalf("no menu of the agent's PRs: %+v, err %q", next.menu, next.err)
	}
	if !strings.Contains(ansi.Strip(next.View()), "#118 Return merge proposals") {
		t.Fatalf("the menu doesn't name them:\n%s", next.View())
	}
	chosen := press(next, "2")
	if chosen.screen != screenPR || chosen.cur.Number != 118 {
		t.Fatalf("choosing didn't open #118: %v", chosen.screen)
	}
	if _, cmd := chosen.Update(keyMsg("esc")); !quits(cmd) {
		t.Fatal("esc on the chosen PR didn't close the popup")
	}
}

// The "pr" action only says where it was pressed: the picker finds the PR,
// so that with none it can answer where you're looking, not in a toast.
func TestPRActionHandsThePaneToThePicker(t *testing.T) {
	var opened map[string]string
	toasts := 0
	old := herdrCallFn
	t.Cleanup(func() { herdrCallFn = old })
	herdrCallFn = func(method string, params any, _ any) error {
		p, _ := params.(map[string]any)
		switch method {
		case "plugin.pane.open":
			opened, _ = p["env"].(map[string]string)
		case "notification.show":
			toasts++
		}
		return nil
	}
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace_id":"w1","workspace_cwd":"/work","focused_pane_id":"p1"}`)
	if err := runAction(context.Background(), withDefaults(config{}), "pr"); err != nil || toasts != 0 {
		t.Fatalf("err %v, toasts %d", err, toasts)
	}
	if opened["HERDR_GITHUB_PANE"] != "p1" || opened["HERDR_GITHUB_SPACE"] != "w1" || opened["HERDR_GITHUB_CWD"] != "/work" {
		t.Fatalf("picker opened with %v", opened)
	}
}

func TestPickerFindsThePanesPR(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	os.MkdirAll(stateDir(), 0o700)
	onBranch := func(n int) {
		t.Helper()
		st := readLabelState()
		st.Branches["github.com/halcyon/notes-app\x00halcyon:feat"] = branchEntry{PR: &prStatus{Number: n, State: "OPEN"}}
		st.Reported["p1"] = reported{Branch: "github.com/halcyon/notes-app\x00halcyon:feat"}
		if err := writeLabelState(st); err != nil {
			t.Fatal(err)
		}
	}
	refreshed := 0
	old := refreshLabelsFn
	t.Cleanup(func() { refreshLabelsFn = old })
	refreshLabelsFn = func(context.Context, config) { refreshed++ }

	// No PR known, and none after looking again: this repo's list, and why.
	m := demoModel(t, tabMine)
	next, cmd := m.openForPane("p1", "w1")
	if view := next.View(); !strings.Contains(view, "Looking for this agent's pull request") || strings.Contains(view, "halcyon/sync-server") {
		t.Fatalf("not looking, or showing the lists meanwhile:\n%s", view)
	}
	next = drive(next, cmd)
	if refreshed != 1 || next.opening != "" || next.screen != screenList || next.tab != tabRepo || !strings.Contains(next.View(), "No pull request found for this agent: these are halcyon/notes-app's") {
		t.Fatalf("refreshed %d, tab %v, screen %v:\n%s", refreshed, next.tab, next.screen, next.View())
	}
	if list := press(next, "down"); list.selected() == nil {
		t.Fatal("the list it fell back to isn't usable")
	}

	// Opened a moment ago: the refresh finds it.
	refreshLabelsFn = func(context.Context, config) { refreshed++; onBranch(479) }
	next, cmd = demoModel(t, tabMine).openForPane("p1", "w1")
	if next = drive(next, cmd); refreshed != 2 || next.screen != screenPR || next.cur.Number != 479 || !next.direct {
		t.Fatalf("the refresh's PR wasn't opened: refreshed %d, screen %v, err %q", refreshed, next.screen, next.err)
	}

	// Known already: straight there, without asking anything again.
	next, _ = demoModel(t, tabMine).openForPane("p1", "w1")
	if refreshed != 2 || next.screen != screenPR || next.cur.Number != 479 {
		t.Fatalf("a known PR waited on a refresh: refreshed %d, screen %v", refreshed, next.screen)
	}
}

func TestPRByNumber(t *testing.T) {
	found := true
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeReq(t, r)
		if req.Variables["n"] != float64(7) || req.Variables["owner"] != "o" || !strings.Contains(req.Query, "pullRequest(number: $n)") {
			t.Errorf("query: %s %v", req.Query, req.Variables)
		}
		if !found {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": nil}},
				"errors": []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve to a PullRequest"}}})
			return
		}
		reply(w, map[string]any{"repository": map[string]any{"pullRequest": prNode(7)}})
	})
	c := newClient("github.com")
	pr, err := c.prByNumber(context.Background(), repoRef{"github.com", "o", "r"}, 7)
	if err != nil || pr.Number != 7 || pr.Host != "github.com" || pr.ID == "" {
		t.Fatalf("got %+v, %v", pr, err)
	}
	found = false
	if _, err := c.prByNumber(context.Background(), repoRef{"github.com", "o", "r"}, 7); err == nil {
		t.Fatal("a PR that isn't there came back")
	}
}
