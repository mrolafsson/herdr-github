package main

import (
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
)

// stdout captures what's written to os.Stdout while f runs.
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestCopyOverSSHAsksTheTerminal(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
	var term bool
	var err error
	out := stdout(t, func() { term, err = copyText("hello") })
	if err != nil || !term {
		t.Fatalf("term=%v err=%v", term, err)
	}
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello")) + "\x07"; out != want {
		t.Fatalf("wrote %q, want %q", out, want)
	}
	if !strings.Contains(copied("the link", term), "terminal") {
		t.Error("says it copied, when it only asked")
	}
}

// Where no browser can be opened in front of you, "open" copies the link.
func TestOpenRemotelyCopiesTheLink(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
	oldNo, oldB := noBrowser, browse
	noBrowser = func() bool { return true }
	browse = func(string) error { t.Error("opened a browser"); return nil }
	t.Cleanup(func() { noBrowser, browse = oldNo, oldB })
	m := model{cfg: config{Hosts: []string{"github.com"}}}
	out := stdout(t, func() { m.openURL("https://github.com/o/r/pull/1") })
	if !strings.Contains(out, "\x1b]52;c;") || !strings.Contains(m.flash, "open it in your browser") || m.err != "" {
		t.Fatalf("out %q, flash %q, err %q", out, m.flash, m.err)
	}
}

func TestOpenLocallyUsesTheBrowser(t *testing.T) {
	oldNo, oldB := noBrowser, browse
	noBrowser = func() bool { return false }
	var opened string
	browse = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { noBrowser, browse = oldNo, oldB })
	m := model{cfg: config{Hosts: []string{"github.com"}}}
	m.openURL("https://github.com/o/r/pull/1")
	if opened != "https://github.com/o/r/pull/1" || m.err != "" {
		t.Fatalf("opened %q, err %q", opened, m.err)
	}
	m.openURL("file:///etc/passwd")
	if opened != "https://github.com/o/r/pull/1" || m.err == "" {
		t.Fatalf("opened a non-GitHub link: %q", opened)
	}
}
