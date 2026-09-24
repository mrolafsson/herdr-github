package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCleanStripsEscapeSequences(t *testing.T) {
	cases := map[string]string{
		"plain":                             "plain",
		"clip\x1b]52;c;ZXZpbA==\x07board":   "clipboard",   // OSC 52 clipboard write, BEL-terminated
		"title\x1b]0;pwned\x1b\\ok":         "titleok",     // OSC with ST terminator
		"\x1b[2J\x1b[Hfake prompt":          "fake prompt", // clear screen + home
		"red\x1b[31mtext\x1b[0m":            "redtext",     // SGR
		"c1\u009b31mcsi":                    "c1csi",       // 8-bit CSI
		"osc8\x1b]8;;https://evil\x07link":  "osc8link",    // hyperlink
		"reset\x1bcdone":                    "resetdone",   // two-byte ESC c
		"dcs\x1bPq#0;2;0;0;0\x1b\\after":    "dcsafter",    // DCS (sixel)
		"bell\x07 and nul\x00 and del\x7f.": "bell and nul and del.",
		"unterminated\x1b]52;c;AAAA":        "unterminated",
		"trailing esc\x1b":                  "trailing esc",
		"Café ✓ 日本 ◔":                       "Café ✓ 日本 ◔", // printable Unicode survives
	}
	for in, want := range cases {
		if got := clean(in, false); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanLineBreaks(t *testing.T) {
	if got := clean("a\nb\tc\r\nd", false); got != "a b c d" {
		t.Errorf("one-line: %q", got)
	}
	if got := clean("a\nb\tc\r\nd\re", true); got != "a\nb\tc\nde" {
		t.Errorf("prose: %q", got)
	}
}

// End to end: hostile text in every field GitHub returns never reaches the
// screen as an escape sequence.
func TestHostileGitHubDataRendersInert(t *testing.T) {
	const osc52 = "\x1b]52;c;cm0gLXJmIH4=\x07"
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"search": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false},
			"nodes": []any{map[string]any{
				"id": "PR_1", "number": 7, "title": "Fix\x1b[2Jit" + osc52 + "\nnow", "url": "https://github.com/o/r/pull/7",
				"state": "OPEN", "author": map[string]any{"login": "mallory" + osc52},
				"repository":  map[string]any{"nameWithOwner": "o/r" + osc52},
				"headRefName": "b" + osc52, "baseRefName": "main",
				"labels": map[string]any{"nodes": []any{map[string]any{"name": "bug" + osc52, "color": "d73a4a"}}},
			}},
		}}})
	})
	prs, err := newClient("github.com").search(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), withDefaults(config{}), newGitHubSource(withDefaults(config{}), nil), "", nil)
	m.width, m.height, m.tab = 100, 20, tabReview
	next, _ := m.Update(listMsg{tab: tabReview, prs: prs})
	view := next.(model).View()
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\n now"} {
		if strings.Contains(view, bad) {
			t.Fatalf("hostile sequence %q reached the screen", bad)
		}
	}
	if pr := prs[0]; pr.Title != "Fixit now" || pr.author() != "mallory" || pr.Repository.NameWithOwner != "o/r" || pr.Labels.Nodes[0].Name != "bug" {
		t.Fatalf("cleaned PR: %+v", pr)
	}
}

// openDemoPRWithBody opens the first PR's screen with body as its description.
func openDemoPRWithBody(t *testing.T, body string) string {
	t.Helper()
	m := demoModel(t, tabMine)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	d := &prDetail{pullRequest: *m.cur, Body: body, MergeStateStatus: "CLEAN"}
	next, _ = m.Update(detailMsg{key: m.cur.key(), detail: d, gen: m.gen})
	return next.(model).View()
}

// The Markdown renderer decodes HTML entities, so an escape spelled &#27;
// passes input cleaning and becomes real afterwards. The final frame must
// still carry no escape but colour/style.
func TestEntityEncodedEscapesNeverReachTheScreen(t *testing.T) {
	// The 8-bit OSC goes last: HTML decodes its "terminator" to œ, so the
	// filter rightly drops everything after it rather than guess its end.
	frame := openDemoPRWithBody(t, "Clip &#27;]52;c;ZXZpbA==&#7; and `&#x1b;[2J` and &#155;31m done &#x9d;0;t&#x9c;")
	if !strings.Contains(stripStyles(frame), "done") {
		t.Fatalf("description not rendered:\n%s", stripStyles(frame))
	}
	for i := 0; i < len(frame); i++ {
		if frame[i] != 0x1b {
			if c := rune(frame[i]); c < 0x20 && c != '\n' {
				t.Fatalf("control byte %#x on screen", c)
			}
			continue
		}
		j := strings.IndexByte(frame[i:], 'm')
		if frame[i+1] != '[' || j < 0 || !sgrParams([]rune(frame[i+2:i+j])) {
			t.Fatalf("non-SGR escape on screen: %q", frame[i:min(len(frame), i+12)])
		}
		i += j
	}
	for _, r := range frame {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("C1 control %#x on screen", r)
		}
	}
}

// An entity-encoded *style* escape survives screenSafe (it's valid SGR), so
// it must never be made in the first place. &#27;[8m would hide text.
func TestEntityEncodedStylingIsNotApplied(t *testing.T) {
	frame := openDemoPRWithBody(t, "before `&#27;[8m`hidden after, and `&#x1b;[5m`blink, and a real &amp; and &#169; stay")
	for _, sgr := range []string{"\x1b[8m", "\x1b[5m"} {
		if strings.Contains(frame, sgr) {
			t.Fatalf("collaborator-written %q reached the screen", sgr)
		}
	}
	text := stripStyles(frame)
	for _, want := range []string{"hidden", "blink", "&", "©"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q lost:\n%s", want, text)
		}
	}
}

func TestScreenSafeKeepsStylingOnly(t *testing.T) {
	in := "\x1b[1;38;2;255;0;0mbold red\x1b[0m\n\x1b]52;c;AA\x07x\x1b[2Jy\x1b[?25lz\x07"
	if got := screenSafe(in); got != "\x1b[1;38;2;255;0;0mbold red\x1b[0m\nxyz" {
		t.Fatalf("%q", got)
	}
}

func TestBodyKeepsLineBreaksButNotEscapes(t *testing.T) {
	d := &prDetail{Body: "line one\n\x1b]52;c;AA\x07line two"}
	d.Title = "Title\nx"
	d.Comments = []comment{{Body: "a\nb\x1b[2J"}}
	sanitize(d)
	if d.Body != "line one\nline two" || d.Title != "Title x" || d.Comments[0].Body != "a\nb" {
		t.Fatalf("%q / %q / %q", d.Body, d.Title, d.Comments[0].Body)
	}
}
