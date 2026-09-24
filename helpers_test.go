package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// TestMain keeps the suite away from the real machine: its own state and
// config dirs, no herdr socket, no background processes, a fixed clock.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "herdr-github-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HERDR_PLUGIN_STATE_DIR", dir+"/state")
	os.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir+"/config")
	for _, v := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		os.Unsetenv(v)
	}
	herdrCallFn = func(method string, _ any, _ any) error { return errors.New("no herdr in tests: " + method) }
	spawnTickFn = func() {}
	tokenFn = func(context.Context, string) (string, error) { return "", errors.New("no tokens in tests") }
	fixed := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return fixed }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeGitHub serves the GraphQL endpoint from h, with a token that works.
func fakeGitHub(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	oldE, oldT := endpointFn, tokenFn
	endpointFn = func(string) string { return srv.URL }
	tokenFn = func(context.Context, string) (string, error) { return "tok", nil }
	t.Cleanup(func() {
		srv.Close()
		endpointFn, tokenFn = oldE, oldT
	})
}

// demoModel is the picker on the demo org, tab t loaded, worktrees marked.
func demoModel(t *testing.T, tb tab) model {
	t.Helper()
	d := newDemoSource()
	m := newModel(context.Background(), withDefaults(config{}), d, "", &d.home)
	m.width, m.height, m.tab = 110, 34, tb
	// A blinking cursor is a timer that never ends; drive would wait on it.
	m.filter.Cursor.SetMode(cursor.CursorStatic)
	// As Init does: every tab loads at once.
	for _, x := range []tab{tabMine, tabReview, tabRepo} {
		p, err := d.list(context.Background(), x, "")
		if err != nil {
			t.Fatal(err)
		}
		next, _ := m.Update(listMsg{tab: x, prs: p.prs, gen: m.gen})
		m = next.(model)
	}
	next, _ := m.Update(worktreesMsg{marks: d.worktreeMarks(), gen: m.gen})
	return next.(model)
}

// press sends keys one by one, running whatever each one starts to the end.
func press(m model, keys ...string) model {
	for _, k := range keys {
		next, cmd := m.Update(keyMsg(k))
		m = drive(next.(model), cmd)
	}
	return m
}

// drive runs cmd and everything it leads to, feeding each message back in,
// as Bubble Tea would. Spinner ticks are dropped (they'd go on for ever), and
// so is quitting (the model is what's checked).
func drive(m model, cmd tea.Cmd) model {
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch msg := msg.(type) {
		case nil, spinner.TickMsg, tea.QuitMsg:
			continue
		case tea.BatchMsg:
			queue = append(queue, msg...)
			continue
		}
		next, more := m.Update(msg)
		m = next.(model)
		queue = append(queue, more)
	}
	return m
}

// quits reports whether cmd (run to its end) asks Bubble Tea to quit.
func quits(cmd tea.Cmd) bool {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.QuitMsg:
			return true
		case tea.BatchMsg:
			queue = append(queue, msg...)
		}
	}
	return false
}
