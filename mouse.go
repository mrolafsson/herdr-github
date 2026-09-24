package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// listTop is the first list row's line: under the tabs, the filter and a blank.
const listTop = 3

// hint is one footer entry. Clicking it presses its key.
type hint struct {
	label string
	key   string // "" = not clickable
}

const hintSep = " · "

var (
	defaultStyleHintHot = lipgloss.NewStyle().Bold(true).Underline(true)
	styleHintHot        = defaultStyleHintHot
)

// renderFooter draws the hints dim, with the one under the pointer lit up so
// it reads as clickable.
func (m model) renderFooter(hs []hint) string {
	hot := ""
	if m.mouseY == m.height-1 {
		hot = hintAt(hs, m.mouseX)
	}
	parts := make([]string, len(hs))
	for i, h := range hs {
		if h.key != "" && h.key == hot {
			parts[i] = styleHintHot.Render(h.label)
		} else {
			parts[i] = styleDim.Render(h.label)
		}
	}
	return styleDim.Render(" ") + strings.Join(parts, styleDim.Render(hintSep))
}

// hintAt finds the footer entry under column x, laid out as renderFooter does.
func hintAt(hs []hint, x int) string {
	pos := 1
	for _, h := range hs {
		w := lipgloss.Width(h.label)
		if x >= pos && x < pos+w {
			return h.key
		}
		pos += w + lipgloss.Width(hintSep)
	}
	return ""
}

// keyMsg turns a hint's key back into the key press it stands for.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+o":
		return tea.KeyMsg{Type: tea.KeyCtrlO}
	case "ctrl+r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}
	case "ctrl+w":
		return tea.KeyMsg{Type: tea.KeyCtrlW}
	case "ctrl+t":
		return tea.KeyMsg{Type: tea.KeyCtrlT}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// handleMouse: hovering highlights, one click opens, the wheel scrolls. Footer
// hints and the tabs are buttons.
func (m model) handleMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeSignedOut {
		return m, nil
	}
	m.mouseX, m.mouseY = ev.X, ev.Y
	if ev.Action == tea.MouseActionMotion {
		// Hover highlights, like a launcher: the click then opens what you see.
		if m.mode == modeBusy || m.mode == modeLoading {
			return m, nil
		}
		if i, ok := m.menuAt(ev.Y); ok {
			m.menuCursor = i
		} else if i, ok := m.rowAt(ev.Y); ok {
			m.cursor = i
		}
		return m, nil
	}
	if ev.Action != tea.MouseActionPress {
		return m, nil
	}
	switch ev.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		delta := 1
		if ev.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		switch {
		case m.menu != nil:
			m.menuCursor = max(0, min(m.menuCursor+delta, len(m.menuItems())-1))
		case m.screen == screenList:
			m.move(delta)
		default:
			m.scrollBody(delta)
		}
		return m, nil
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	if m.mode == modeBusy {
		return m, nil
	}
	if ev.Y == 0 && m.menu == nil {
		return m.clickTab(ev.X)
	}
	if ev.Y == m.height-1 {
		if k := hintAt(m.currentFooter(), ev.X); k != "" {
			return m.handleKey(keyMsg(k))
		}
		return m, nil
	}
	if m.mode == modeLoading {
		return m, nil
	}
	if i, ok := m.menuAt(ev.Y); ok {
		if time.Since(m.menuOpened) < menuClickGuard {
			return m, nil
		}
		return m.chooseMenu(i)
	}
	if i, ok := m.rowAt(ev.Y); ok {
		m.cursor, m.flash = i, ""
		return m.handleKey(keyMsg("enter"))
	}
	return m, nil
}

// rowAt is the selectable list row on screen line y, if any.
func (m model) rowAt(y int) (int, bool) {
	if m.screen != screenList || m.menu != nil || y < listTop {
		return 0, false
	}
	rows := m.rows()
	i := m.offset + y - listTop
	if i >= len(rows) || i >= m.offset+m.listHeight() || rows[i].label() {
		return 0, false
	}
	return i, true
}

// menuAt is the menu item on screen line y, if a menu is open.
func (m model) menuAt(y int) (int, bool) {
	if m.menu == nil {
		return 0, false
	}
	top, room := listTop, m.listHeight()
	if m.screen != screenList {
		header := m.prHeader()
		top, room = 2+strings.Count(header, "\n"), m.bodyRoomFor(header) // tabs, blank, header
	}
	row := y - top - menuTop
	i := m.menuStart(room) + row
	if row < 0 || row >= room-menuTop || i >= len(m.menuItems()) {
		return 0, false
	}
	return i, true
}

// clickTab switches to the clicked tab, leaving the PR screen: the tabs are
// the way home.
func (m model) clickTab(x int) (tea.Model, tea.Cmd) {
	pos := 0
	for i, l := range m.tabLabels() {
		w := lipgloss.Width(l)
		if x >= pos && x < pos+w {
			onList := m.screen == screenList
			m.screen, m.err, m.flash, m.curDetail = screenList, "", "", nil
			if tab(i) == m.tab {
				// Clicking the repo tab you're on picks another repo.
				if onList && tab(i) == tabRepo {
					return m.openRepoPicker()
				}
				m.clampCursor()
				return m, nil
			}
			return m.switchTab(tab(i))
		}
		pos += w + 1
	}
	return m, nil
}
