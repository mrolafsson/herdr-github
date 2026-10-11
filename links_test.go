package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Opened from another plugin's popup, esc goes back to it: on the PR it was
// opened on, and from the lists. Going somewhere else doesn't.
func TestEscGoesBackToThePopupItWasOpenedFrom(t *testing.T) {
	var opened []popupRef
	old := handOver
	handOver = func(p popupRef) error { opened = append(opened, p); return nil }
	t.Cleanup(func() { handOver = old })
	t.Setenv(returnEnv, `{"plugin_id":"herdr-recap","entrypoint":"picker","width":"80%","height":"75%","env":{"HERDR_RECAP_PANE":"w1:p4"}}`)

	m := demoModel(t, tabMine)
	m.back = cameFrom()
	if m.back == nil || m.back.Env["HERDR_RECAP_PANE"] != "w1:p4" {
		t.Fatalf("back %+v", m.back)
	}
	next, cmd := m.openOn([]prRef{{repoRef{"github.com", "halcyon", "notes-app"}, 479}})
	next = drive(next, cmd)
	if view := ansi.Strip(next.View()); !strings.Contains(view, "esc back") || strings.Contains(view, "esc close") {
		t.Fatalf("the footer doesn't say esc goes back:\n%s", view)
	}
	if _, cmd := next.Update(keyMsg("esc")); !quits(cmd) || len(opened) != 1 || opened[0].PluginID != "herdr-recap" {
		t.Fatalf("esc on the PR: opened %+v", opened)
	}
	// From the lists too.
	if _, cmd := press(next, "left").Update(keyMsg("esc")); !quits(cmd) || len(opened) != 2 {
		t.Fatalf("esc on the lists: opened %+v", opened)
	}
	// ctrl+c closes the lot.
	if _, cmd := next.Update(keyMsg("ctrl+c")); !quits(cmd) || len(opened) != 2 {
		t.Fatalf("ctrl+c went back: %+v", opened)
	}
}

func TestAPopupFromTheEnvironmentIsHeldToAnID(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"plugin_id":"herdr-recap","entrypoint":"picker"}`: true,
		`{"plugin_id":"../x","entrypoint":"picker"}`:        false,
		`{"plugin_id":"herdr-recap","entrypoint":"a b"}`:    false,
		`{"plugin_id":"herdr-recap"}`:                       false,
		``:                                                  false,
		`not json`:                                          false,
	} {
		t.Setenv(returnEnv, raw)
		if got := cameFrom() != nil; got != want {
			t.Errorf("%q: %v", raw, got)
		}
	}
	// Opened as ever, esc closes it and nothing else opens.
	t.Setenv(returnEnv, "")
	m := demoModel(t, tabMine)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "esc close") {
		t.Errorf("no esc close:\n%s", view)
	}
}
