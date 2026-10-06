package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// listTop is the first list row's line: under the tabs, the filter and a blank.
const listTop = 3

// renderFooter draws the keys along the bottom, the one under the pointer
// lit so it reads as clickable.
func (m model) renderFooter(hs []hint) string {
	hot := ""
	if m.mouseY == m.height-1 {
		hot = hintAt(hs, m.mouseX, m.width)
	}
	return footerLine(hs, hot, m.width)
}

// handleMouse: hovering highlights, one click opens, the wheel scrolls. Footer
// hints and the tabs are buttons.
func (m model) handleMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeSignedOut {
		return m, nil
	}
	m.mouseX, m.mouseY = ev.X, ev.Y
	if m.opening != "" {
		// Only the footer is there to click.
		if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft && ev.Y == m.height-1 {
			if k := hintAt(m.currentFooter(), ev.X, m.width); k != "" {
				return m.handleKey(keyMsg(k))
			}
		}
		return m, nil
	}
	if ev.Action == tea.MouseActionMotion {
		// Hover highlights, like a launcher: the click then opens what you see.
		if m.mode == modeBusy || (m.mode == modeLoading && m.menu == nil && m.screen == screenList) {
			return m, nil
		}
		if i, ok := m.menuAt(ev.Y); ok {
			if !m.menuItems()[i].header {
				m.menuCursor = i
			}
		} else if i, ok := m.rowAt(ev.Y); ok {
			m.cursor = i
			m.wantRepoRow = m.rows()[i].repo
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
			m.moveMenu(delta)
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
		if k := hintAt(m.currentFooter(), ev.X, m.width); k != "" {
			return m.handleKey(keyMsg(k))
		}
		return m, nil
	}
	if m.mode == modeLoading && m.menu == nil && m.screen == screenList {
		return m, nil
	}
	if i, ok := m.menuAt(ev.Y); ok {
		if time.Since(m.menuOpened) < menuClickGuard {
			return m, nil
		}
		return m.chooseMenu(i)
	}
	if i, ok := m.agentAt(ev.Y); ok {
		return m.clickAgent(i)
	}
	if i, ok := m.rowAt(ev.Y); ok {
		m.cursor, m.flash = i, ""
		// On its agent, a click goes to the agent; anywhere else, to the PR.
		if pr := m.rows()[i].pr; pr != nil && m.pointerOnAgent(pr) {
			return m.goToAgent(*pr)
		}
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
	top, room := listTop, m.menuRoom()
	if m.screen != screenList {
		top = 2 + strings.Count(m.prHeader(), "\n") // tabs, blank, header
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
			m.screen, m.err, m.flash, m.curDetail, m.direct = screenList, "", "", nil, false
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
