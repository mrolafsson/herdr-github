package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type mode int

const (
	modeLoading mode = iota
	modeSignedOut
	modeList
	modeBusy
)

// ── messages ──────────────────────────────────────────────────────────────────

// A load's reply carries the gen it was asked for under, so one that arrives
// after a refresh or sign-in is dropped rather than shown over newer data.
type listMsg struct {
	tab    tab
	cursor string // the page asked for: "" is the first
	prs    []pullRequest
	next   string // the page after, "" at the end
	err    error
	gen    int
}

type worktreesMsg struct {
	marks map[string]bool   // pr.key() → its worktree exists
	dirs  map[string]string // repo key → local checkout
	gen   int
}

// noteMsg puts a line in the status line without stopping anything.
type noteMsg string

// actionDoneMsg ends a worktree action: success closes the popup (you're in
// the worktree now); a note or error keeps it open to be read.
type actionDoneMsg struct {
	err  error
	note string
}

// needCloneMsg: the PR's repo has no checkout here yet.
type needCloneMsg struct {
	pr    pullRequest
	start bool
	err   *noCheckoutError
}

type loginDoneMsg struct{ err error }

// ── styles ────────────────────────────────────────────────────────────────────

// The defaults, for a theme that leaves a colour unset; useTheme recolours
// the styles from herdr's theme.
var (
	defaultStyleDim      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"})
	defaultStyleHeader   = lipgloss.NewStyle().Bold(true)
	defaultStyleTabOn    = lipgloss.NewStyle().Bold(true).Underline(true)
	defaultStyleSelected = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "254", Dark: "237"})
	defaultStyleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149"))
	defaultStyleUrgent   = lipgloss.NewStyle().Foreground(lipgloss.Color("#db6d28")).Bold(true)
	defaultStyleTree     = lipgloss.NewStyle().Foreground(lipgloss.Color("#4493f8"))
	defaultStyleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950"))
	defaultStyleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922"))
	defaultStyleMerged   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ab7df8"))
)

var (
	styleDim      = defaultStyleDim
	styleHeader   = defaultStyleHeader
	styleTabOn    = defaultStyleTabOn
	styleSelected = defaultStyleSelected
	styleErr      = defaultStyleErr
	styleUrgent   = defaultStyleUrgent
	styleTree     = defaultStyleTree
	styleOK       = defaultStyleOK
	styleWarn     = defaultStyleWarn
	styleMerged   = defaultStyleMerged
)

// prIcon is the PR's state, in GitHub's colours for it: open green, draft
// grey, merged purple, closed red. Every glyph is one common monospace fonts
// include, so the columns stay straight.
func prIcon(pr pullRequest) string {
	switch {
	case pr.State == "MERGED":
		return styleMerged.Render("◆")
	case pr.State == "CLOSED":
		return styleErr.Render("⊘")
	case pr.IsDraft:
		return styleDim.Render("◌")
	}
	return styleOK.Render("●")
}

func prStateName(pr pullRequest) string {
	switch {
	case pr.State == "MERGED":
		return styleMerged.Render("Merged")
	case pr.State == "CLOSED":
		return styleErr.Render("Closed")
	case pr.IsDraft:
		return styleDim.Render("Draft")
	}
	return styleOK.Render("Open")
}

// checksBadge is the head commit's checks as one coloured glyph.
func checksBadge(state string) string {
	g := checkGlyph(state)
	switch g {
	case "✓":
		return styleOK.Render(g)
	case "✗":
		return styleErr.Render(g)
	case "●":
		return styleWarn.Render(g)
	}
	return ""
}

func colored(glyph, hex string) string {
	if hex == "" {
		return glyph
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(glyph)
}

// ── model ─────────────────────────────────────────────────────────────────────

type row struct {
	header string // non-empty: a group heading, not selectable
	spacer bool   // the blank line above a heading, not selectable
	pr     *pullRequest
}

// label rows are there to be read, never selected.
func (r row) label() bool { return r.header != "" || r.spacer }

type model struct {
	ctx     context.Context
	cfg     config
	client  source
	demo    bool
	invoked string   // cwd of the space the picker was opened from
	repo    *repoRef // its GitHub repo, if it is one
	gen     int
	width   int
	height  int
	mode    mode
	tab     tab
	filter  textinput.Model
	spin    spinner.Model
	status  string // what's in progress
	err     string // last error, shown above the footer
	flash   string // a one-line confirmation
	host    string // the host to sign in to, on the signed-out screen

	prs       map[tab][]pullRequest
	loaded    map[tab]bool      // a list is there to show (fresh or cached)
	stale     map[tab]bool      // it's the cached one; the fresh one is on its way
	paging    map[tab]bool      // more pages are loading
	tabErr    map[tab]string    // why a tab's list couldn't load
	worktrees map[string]bool   // pr.key() → its worktree exists
	checkouts map[string]string // repo key → local checkout
	cursor    int
	offset    int

	// The PR screen (detail.go).
	screen    screen
	cur       *pullRequest
	curDetail *prDetail
	md        *markdown
	scroll    int

	// A choice over the current screen: merge method, confirmations.
	menu       *menu
	menuCursor int

	mouseX, mouseY int // last pointer position; -1 until the mouse moves
}

func newModel(ctx context.Context, cfg config, client source, invoked string, repo *repoRef) model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "filter"
	ti.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	_, demo := client.(*demoSource)
	return model{
		ctx: ctx, cfg: cfg, client: client, demo: demo, invoked: invoked, repo: repo,
		mode: modeLoading, filter: ti, spin: sp,
		prs: map[tab][]pullRequest{}, loaded: map[tab]bool{}, stale: map[tab]bool{},
		paging: map[tab]bool{}, tabErr: map[tab]string{},
		worktrees: map[string]bool{}, checkouts: map[string]string{},
		md: newMarkdown(cfg.Theme != "light"), mouseX: -1, mouseY: -1,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.loadAll())
}

// useCache shows the lists the last popup loaded until fresh ones arrive.
func (m model) useCache(lists map[tab][]pullRequest) model {
	for t, prs := range lists {
		m.prs[t], m.loaded[t], m.stale[t] = prs, true, true
	}
	if m.loaded[m.tab] {
		m.mode = modeList
		m.clampCursor()
	}
	return m
}

// loadAll asks for every tab at once, so switching tabs doesn't wait.
func (m model) loadAll() tea.Cmd {
	cmds := []tea.Cmd{m.loadTab(tabMine, ""), m.loadTab(tabReview, "")}
	if m.repo != nil {
		cmds = append(cmds, m.loadTab(tabRepo, ""))
	}
	return tea.Batch(cmds...)
}

func (m model) loadTab(t tab, cursor string) tea.Cmd {
	client, ctx, gen := m.client, m.ctx, m.gen
	return func() tea.Msg {
		debugf("loadTab start tab=%d cursor=%q", t, cursor)
		p, err := client.list(ctx, t, cursor)
		return listMsg{tab: t, cursor: cursor, prs: p.prs, next: p.next, err: err, gen: gen}
	}
}

// mergePRs adds a page to a list, without doubles (a PR updated between two
// pages can move from one to the next).
func mergePRs(list, more []pullRequest) []pullRequest {
	seen := map[string]bool{}
	for _, pr := range list {
		seen[pr.key()] = true
	}
	for _, pr := range more {
		if !seen[pr.key()] {
			seen[pr.key()] = true
			list = append(list, pr)
		}
	}
	return list
}

// selectedKey and reselect keep the cursor on the same PR while the list
// under it changes.
func (m model) selectedKey() string {
	if r := m.selected(); r != nil {
		return r.pr.key()
	}
	return ""
}

func (m *model) reselect(key string) {
	if key != "" {
		for i, r := range m.rows() {
			if r.pr != nil && r.pr.key() == key {
				m.cursor = i
				m.scrollTo()
				return
			}
		}
	}
	m.clampCursor()
}

// loadWorktrees finds which PRs already have a worktree, by looking for each
// repo's checkout and listing its worktrees.
func (m model) loadWorktrees(prs []pullRequest) tea.Cmd {
	if len(prs) == 0 {
		return nil
	}
	if d, ok := m.client.(*demoSource); ok {
		gen := m.gen
		return func() tea.Msg { return worktreesMsg{d.worktreeMarks(), nil, gen} }
	}
	ctx, cfg, invoked, gen := m.ctx, m.cfg, m.invoked, m.gen
	return func() tea.Msg {
		marks, dirs := map[string]bool{}, map[string]string{}
		byRepo := map[string][]pullRequest{}
		for _, pr := range prs {
			byRepo[pr.repo().key()] = append(byRepo[pr.repo().key()], pr)
		}
		for key, list := range byRepo {
			dir, err := findCheckout(ctx, cfg, list[0].repo(), invoked)
			if err != nil {
				continue
			}
			dirs[key] = dir
			wts, err := listWorktrees(dir)
			if err != nil {
				continue
			}
			have := map[string]bool{}
			for _, w := range wts {
				have[strings.TrimPrefix(w.Branch, "refs/heads/")] = true
			}
			for _, pr := range list {
				marks[pr.key()] = have[localBranch(pr)]
			}
		}
		return worktreesMsg{marks, dirs, gen}
	}
}

// ── update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if next, cmd, handled := m.updateDetail(msg); handled {
		return next, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.filter.Width = max(10, msg.Width-6)
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case listMsg:
		debugf("listMsg tab=%d cursor=%q n=%d next=%q err=%v gen=%d/%d", msg.tab, msg.cursor, len(msg.prs), msg.next, msg.err, msg.gen, m.gen)
		if msg.gen != m.gen {
			return m, nil
		}
		t := msg.tab
		if msg.tab == m.tab && m.mode == modeLoading {
			m.mode = modeList
		}
		if msg.err != nil && len(msg.prs) == 0 {
			var so *signedOutError
			if errors.As(msg.err, &so) {
				m.mode, m.host = modeSignedOut, so.Host
				return m, nil
			}
			// A cached list stays up, marked as such, with the reason.
			m.loaded[t], m.paging[t], m.tabErr[t] = true, false, msg.err.Error()
			return m, nil
		}
		keep := m.selectedKey()
		if msg.cursor == "" {
			m.prs[t] = msg.prs
		} else {
			m.prs[t] = mergePRs(m.prs[t], msg.prs)
			sortPRs(m.prs[t], t)
		}
		m.loaded[t], m.stale[t], m.tabErr[t] = true, false, ""
		if msg.err != nil {
			m.tabErr[t] = msg.err.Error() // usable, but not complete
		}
		if t == m.tab && m.screen == screenList {
			m.reselect(keep)
		}
		cmds := []tea.Cmd{m.loadWorktrees(msg.prs)}
		switch {
		case msg.next != "" && len(m.prs[t]) < listCap:
			m.paging[t] = true
			cmds = append(cmds, m.loadTab(t, msg.next))
		case msg.next != "":
			m.paging[t], m.tabErr[t] = false, errTruncated.Error()
		default:
			m.paging[t] = false
			if !m.demo && msg.err == nil {
				prs, repo := append([]pullRequest(nil), m.prs[t]...), m.repo
				cmds = append(cmds, func() tea.Msg { saveListCache(t, prs, repo); return nil })
			}
		}
		return m, tea.Batch(cmds...)

	case worktreesMsg:
		if msg.gen == m.gen {
			for k, v := range msg.marks {
				m.worktrees[k] = v
			}
			for k, v := range msg.dirs {
				m.checkouts[k] = v
			}
		}
		return m, nil

	case noteMsg:
		m.err = string(msg)
		return m, nil

	case loginDoneMsg:
		if msg.err != nil {
			m.err = "gh auth login: " + msg.err.Error()
		}
		return m.refreshAll()

	case needCloneMsg:
		m.mode = modeList
		return m.openMenu(cloneMenu(msg)), nil

	case demoDoneMsg:
		// The demo stays open after an action so you can keep exploring.
		m.mode = modeList
		m.flash = msg.note
		if msg.key != "" {
			m.worktrees[msg.key] = true
		}
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.mode, m.err = modeList, msg.err.Error()
			return m, nil
		}
		if msg.note != "" {
			// Something was left undone: say it here, not in a toast that
			// might never show, and let you close the popup once you've read it.
			m.mode, m.err = modeList, msg.note
			return m, nil
		}
		return m, tea.Quit

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// refreshAll forgets every tab and loads the current one again.
func (m model) refreshAll() (model, tea.Cmd) {
	m.gen++
	m.prs, m.loaded, m.stale, m.paging, m.tabErr = map[tab][]pullRequest{}, map[tab]bool{}, map[tab]bool{}, map[tab]bool{}, map[tab]string{}
	m.mode, m.err = modeLoading, ""
	if gs, ok := m.client.(*githubSource); ok {
		// A new sign-in means new tokens: start the clients afresh.
		m.client = newGitHubSource(m.cfg, gs.repo)
	}
	return m, tea.Batch(m.spin.Tick, m.loadAll())
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeSignedOut:
		switch k.String() {
		case "enter":
			return m, m.login()
		case "esc", "q":
			return m, tea.Quit
		}
		return m, nil
	case modeBusy:
		return m, nil
	case modeLoading:
		switch k.String() {
		case "esc":
			return m, tea.Quit
		case "tab", "shift+tab", "left", "right":
			// A load in flight doesn't lock the tabs; its reply still lands.
			if m.screen != screenList || m.menu != nil {
				return m, nil
			}
		default:
			return m, nil
		}
	}
	if m.menu != nil {
		return m.handleMenuKey(k)
	}
	if m.screen != screenList {
		return m.handleDetailKey(k)
	}
	m.flash = ""

	switch k.String() {
	case "esc":
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.cursor, m.offset = 0, 0
			m.clampCursor()
			return m, nil
		}
		return m, tea.Quit
	case "tab", "shift+tab":
		next := (m.tab + 1) % 3
		if k.String() == "shift+tab" {
			next = (m.tab + 2) % 3
		}
		return m.switchTab(next)
	case "left", "right":
		// With text in the filter, arrows edit it; otherwise they switch tabs.
		if m.filter.Value() != "" {
			break
		}
		if k.String() == "right" {
			return m.switchTab((m.tab + 1) % 3)
		}
		return m.switchTab((m.tab + 2) % 3)
	case "up", "ctrl+p", "ctrl+k":
		m.move(-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.move(1)
		return m, nil
	case "pgup":
		m.move(-m.listHeight())
		return m, nil
	case "pgdown":
		m.move(m.listHeight())
		return m, nil
	case "ctrl+r":
		return m.reloadLists()
	case "ctrl+o":
		if r := m.selected(); r != nil {
			m.openURL(r.pr.URL)
		}
		return m, nil
	case "enter":
		if r := m.selected(); r != nil {
			return m.openPR(*r.pr)
		}
		return m, nil
	case "ctrl+w":
		if r := m.selected(); r != nil {
			return m.runWorktree(*r.pr, false)
		}
		return m, nil
	case "ctrl+s":
		if r := m.selected(); r != nil {
			return m.runWorktree(*r.pr, true)
		}
		return m, nil
	}

	// Anything else edits the filter.
	prev := m.filter.Value()
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(k)
	if m.filter.Value() != prev {
		m.cursor, m.offset = 0, 0
		m.clampCursor()
	}
	return m, cmd
}

func (m model) switchTab(t tab) (tea.Model, tea.Cmd) {
	m.tab, m.err, m.flash = t, "", ""
	if !m.demo {
		rememberTab(t)
	}
	m.cursor, m.offset = 0, 0
	switch {
	case t == tabRepo && m.repo == nil:
		m.loaded[t] = true // nothing to load: the empty text says why
		m.mode = modeList
	case !m.loaded[t]:
		m.mode = modeLoading // it was asked for with the others
	default:
		m.mode = modeList
	}
	m.clampCursor()
	return m, nil
}

// reloadLists asks GitHub for every tab again; what's shown stays up,
// marked stale, until the new lists arrive.
func (m model) reloadLists() (model, tea.Cmd) {
	m.gen++
	m.err, m.tabErr = "", map[tab]string{}
	for t := range m.loaded {
		m.stale[t] = true
	}
	return m, tea.Batch(m.spin.Tick, m.loadAll())
}

// login runs `gh auth login` in the popup itself: it's a terminal, and gh's
// prompts and browser hand-off work as they would anywhere else.
func (m model) login() tea.Cmd {
	host := m.host
	if host == "" {
		host = "github.com"
	}
	return tea.ExecProcess(exec.Command("gh", "auth", "login", "--hostname", host, "--web"), func(err error) tea.Msg {
		return loginDoneMsg{err}
	})
}

// runWorktree opens or creates the PR's worktree (start: and prompts its new
// agent). It finds the repo's checkout first; with none, it offers to clone.
func (m model) runWorktree(pr pullRequest, start bool) (tea.Model, tea.Cmd) {
	verb := "Opening"
	if !m.worktrees[pr.key()] {
		verb = "Creating a worktree for"
	}
	m.mode, m.err, m.flash, m.status = modeBusy, "", "", fmt.Sprintf("%s #%d…", verb, pr.Number)
	if d, ok := m.client.(*demoSource); ok {
		return m, tea.Batch(m.spin.Tick, func() tea.Msg { return d.worktree(pr, start) })
	}
	ctx, cfg, invoked := m.ctx, m.cfg, m.invoked
	known := m.checkouts[pr.repo().key()]
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		dir := known
		if dir == "" {
			var err error
			dir, err = findCheckout(ctx, cfg, pr.repo(), invoked)
			var nc *noCheckoutError
			if errors.As(err, &nc) {
				return needCloneMsg{pr, start, nc}
			} else if err != nil {
				return actionDoneMsg{err: err}
			}
		}
		return doWorktree(ctx, cfg, pr, dir, start)
	})
}

func cloneMenu(msg needCloneMsg) *menu {
	pr, start, r := msg.pr, msg.start, msg.err.Repo
	return &menu{
		title: "There's no checkout of " + r.String() + " here yet.",
		items: []menuItem{
			{label: "Clone it into " + tildePath(msg.err.Dest), run: func(m model) (tea.Model, tea.Cmd) {
				m.mode, m.status = modeBusy, "Cloning "+r.String()+"…"
				ctx, cfg := m.ctx, m.cfg
				return m, tea.Batch(m.spin.Tick, func() tea.Msg {
					dir, err := clone(ctx, cfg, r)
					if err != nil {
						return actionDoneMsg{err: err}
					}
					return doWorktree(ctx, cfg, pr, dir, start)
				})
			}},
			{label: "Cancel", run: func(m model) (tea.Model, tea.Cmd) {
				m.err = "To use a checkout that's elsewhere, map it under \"repos\" in the plugin's config.json."
				return m, nil
			}},
		},
	}
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// ── rows + cursor ─────────────────────────────────────────────────────────────

func (m model) rows() []row {
	q := m.filter.Value()
	var rows []row
	last := "\x00"
	list := m.prs[m.tab]
	for i := range list {
		pr := &list[i]
		if !pr.matches(q) {
			continue
		}
		group := m.groupOf(*pr)
		if group != last {
			if len(rows) > 0 {
				rows = append(rows, row{spacer: true})
			}
			rows = append(rows, row{header: group})
			last = group
		}
		rows = append(rows, row{pr: pr})
	}
	return rows
}

// groupOf is the heading a PR is listed under: its repo, or in the repo tab
// whether it's ready or a draft.
func (m model) groupOf(pr pullRequest) string {
	if m.tab == tabRepo {
		if pr.IsDraft {
			return "Drafts"
		}
		return "Open"
	}
	if pr.Host != "github.com" && pr.Host != "" {
		return pr.Host + "/" + pr.Repository.NameWithOwner
	}
	return pr.Repository.NameWithOwner
}

func (m model) selected() *row {
	rows := m.rows()
	if m.cursor >= 0 && m.cursor < len(rows) && !rows[m.cursor].label() {
		return &rows[m.cursor]
	}
	return nil
}

func (m *model) move(delta int) {
	rows := m.rows()
	if len(rows) == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	c := min(max(m.cursor+delta, 0), len(rows)-1)
	// Skip headings and spacers, in the direction of travel, then back if we ran off.
	for c >= 0 && c < len(rows) && rows[c].label() {
		c += step
	}
	if c < 0 || c >= len(rows) {
		c = m.cursor
	}
	m.cursor = c
	m.scrollTo()
}

func (m *model) clampCursor() {
	rows := m.rows()
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	for m.cursor < len(rows) && rows[m.cursor].label() {
		m.cursor++
	}
	m.scrollTo()
}

func (m *model) scrollTo() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
		// Keep the group heading above the first row visible.
		if m.offset > 0 {
			if rows := m.rows(); rows[m.offset-1].header != "" {
				m.offset--
			}
		}
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m model) listHeight() int {
	// tabs + filter + blank above; status line + footer below, the footer on
	// the popup's last line (mouse.go counts on that).
	return max(3, m.height-listTop-2)
}

// ── view ──────────────────────────────────────────────────────────────────────

// View is what Bubble Tea writes to the terminal. Every frame passes through
// screenSafe, whatever path its text took to get there.
func (m model) View() string {
	return screenSafe(m.view())
}

func (m model) view() string {
	if m.width == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.viewTabs() + "\n")

	if m.mode == modeSignedOut {
		host := m.host
		if host == "" {
			host = "github.com"
		}
		b.WriteString("\n  " + styleHeader.Render("GitHub isn't connected") + ": gh has no working sign-in for " + host + ".\n\n")
		b.WriteString("  Press enter to run " + styleTabOn.Render("gh auth login") + " here; it opens your browser.\n")
		b.WriteString(styleDim.Render("  The plugin uses gh's token and keeps none of its own.") + "\n")
		if m.err != "" {
			b.WriteString("\n  " + styleErr.Render(shorten(clean(m.err, false), max(10, m.width-4))) + "\n")
		}
		return b.String()
	}

	if m.screen != screenList {
		b.WriteString("\n")
		b.WriteString(m.viewPR())
		// Pin the status line and footer to the bottom.
		for n := strings.Count(b.String(), "\n"); n < m.height-2; n++ {
			b.WriteString("\n")
		}
		b.WriteString(m.statusLine())
		b.WriteString(m.renderFooter(m.currentFooter()))
		return b.String()
	}

	b.WriteString(m.filter.View() + "\n\n")
	h := m.listHeight()
	if m.menu != nil {
		menu := m.viewMenu(h)
		b.WriteString(menu)
		for n := strings.Count(menu, "\n"); n < h; n++ {
			b.WriteString("\n")
		}
	} else {
		rows := m.rows()
		switch {
		case m.mode == modeLoading && len(rows) == 0:
			b.WriteString("  " + m.spin.View() + " Loading…\n")
			h--
		case len(rows) == 0:
			b.WriteString(styleDim.Render("  "+m.emptyText()) + "\n")
			h--
		}
		for i := m.offset; i < len(rows) && i < m.offset+h; i++ {
			b.WriteString(m.viewRow(rows[i], i == m.cursor) + "\n")
		}
		for i := len(rows) - m.offset; i < h; i++ {
			b.WriteString("\n")
		}
	}

	b.WriteString(m.statusLine())
	b.WriteString(m.renderFooter(m.currentFooter()))
	return b.String()
}

func (m model) emptyText() string {
	switch {
	case m.filter.Value() != "":
		return "Nothing matches."
	case m.tab == tabMine:
		return "You have no open pull requests."
	case m.tab == tabReview:
		return "Nobody is waiting on your review."
	case m.repo == nil:
		return "Open this from a space inside a GitHub repo to see its pull requests."
	}
	return "No open pull requests in " + m.repo.String() + "."
}

// statusLine is the one line above the footer: work in progress, the last
// error, or a confirmation.
func (m model) statusLine() string {
	switch {
	case m.mode == modeBusy:
		return " " + m.spin.View() + " " + m.status + "\n"
	case m.err != "":
		return " " + styleErr.Render(shorten(clean(m.err, false), max(10, m.width-2))) + "\n"
	case m.flash != "":
		return " " + styleOK.Render("✓ ") + shorten(clean(m.flash, false), max(10, m.width-4)) + "\n"
	case m.screen == screenList && m.tabErr[m.tab] != "":
		return " " + styleErr.Render(shorten(clean(m.tabErr[m.tab], false), max(10, m.width-2))) + "\n"
	}
	return "\n"
}

// tabLabels are the tabs as drawn, so clicks can be measured against them.
func (m model) tabLabels() []string {
	repo := "This repo"
	if m.repo != nil {
		repo = m.repo.Name
	}
	names := []string{"Mine", "Review requested", repo}
	out := make([]string, len(names))
	for i, n := range names {
		if m.loaded[tab(i)] && (tab(i) != tabRepo || m.repo != nil) {
			n += fmt.Sprintf(" %d", len(m.prs[tab(i)]))
			if m.paging[tab(i)] {
				n += "+"
			}
		}
		out[i] = " " + n + " "
	}
	return out
}

func (m model) viewTabs() string {
	var parts []string
	for i, l := range m.tabLabels() {
		if tab(i) == m.tab && m.screen == screenList {
			parts = append(parts, styleTabOn.Render(l))
		} else {
			parts = append(parts, styleDim.Render(l))
		}
	}
	line := strings.Join(parts, " ")
	if m.screen != screenList && m.cur != nil {
		line += styleDim.Render(" › ") + styleHeader.Render(fmt.Sprintf("%s#%d", m.cur.Repository.NameWithOwner, m.cur.Number))
	}
	updating := false
	for t := range m.stale {
		updating = updating || m.stale[t] || m.paging[t]
	}
	if updating && m.mode != modeBusy {
		tag := styleDim.Render(m.spin.View() + " updating ")
		if pad := m.width - lipgloss.Width(line) - lipgloss.Width(tag); pad > 1 {
			line += strings.Repeat(" ", pad) + tag
		}
	} else if m.demo {
		tag := styleDim.Render("demo ")
		if pad := m.width - lipgloss.Width(line) - lipgloss.Width(tag); pad > 1 {
			line += strings.Repeat(" ", pad) + tag
		}
	}
	return line
}

func (m model) viewRow(r row, selected bool) string {
	w := m.width
	if r.spacer {
		return ""
	}
	if r.header != "" {
		return styleDim.Render("  " + r.header)
	}
	pr := r.pr
	num := fmt.Sprintf("#%-*d", m.numWidth(), pr.Number)
	left := " " + prIcon(*pr) + " " + styleDim.Render(num) + " "

	var meta []string
	if m.tab != tabMine {
		meta = append(meta, styleDim.Render(pr.author()))
	}
	switch {
	case pr.Mergeable == "CONFLICTING":
		meta = append(meta, styleErr.Render("conflicts"))
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		meta = append(meta, styleErr.Render("changes"))
	case pr.ReviewDecision == "APPROVED":
		meta = append(meta, styleOK.Render("approved"))
	}
	if pr.AutoMergeRequest != nil {
		meta = append(meta, styleTree.Render("auto"))
	}
	if c := checksBadge(pr.checks()); c != "" {
		meta = append(meta, c)
	}
	right := strings.Join(meta, styleDim.Render(" · "))
	if m.worktrees[pr.key()] {
		right += " " + styleTree.Render("⌥")
	} else {
		right += "  "
	}
	return m.fitRow(left, pr.Title, right, w, selected)
}

// numWidth is the width of the longest PR number in the tab, so titles line up.
func (m model) numWidth() int {
	n := 1
	for _, pr := range m.prs[m.tab] {
		n = max(n, len(fmt.Sprint(pr.Number)))
	}
	return n
}

// fitRow lays out left + title + right-aligned meta in exactly w cells,
// truncating the title (never the number) when space runs out.
func (m model) fitRow(left, title, right string, w int, selected bool) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	room := w - lw - rw - 2
	if room < 8 {
		right, rw = "", 0
		room = w - lw - 1
	}
	title = shorten(title, max(1, room))
	pad := max(1, w-lw-lipgloss.Width(title)-rw-1)
	line := left + title + strings.Repeat(" ", pad) + right
	if selected {
		return highlight(line, w)
	}
	return line
}

// highlight gives a whole row the selection background, w cells wide. The
// row's own coloured pieces each end in a style reset, which would also end
// the background, so it's switched back on after every one.
func highlight(line string, w int) string {
	on := styleSelected.Render("x")
	on = on[:strings.Index(on, "x")] // the escape that turns the background on
	if on != "" {
		line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+on)
	}
	return styleSelected.Width(w).Render(line)
}

func (m model) currentFooter() []hint {
	switch {
	case m.menu != nil:
		return []hint{{"↑↓ choose", ""}, {"enter select", "enter"}, {"esc cancel", "esc"}}
	case m.screen != screenList:
		return m.detailFooter()
	}
	return []hint{
		{"enter details", "enter"}, {"^w worktree", "ctrl+w"}, {"^s start", "ctrl+s"},
		{"^o open", "ctrl+o"}, {"^r refresh", "ctrl+r"}, {"tab switch", "tab"}, {"esc close", "esc"},
	}
}

// ── menus ─────────────────────────────────────────────────────────────────────

// menu is a short list of choices shown over the current screen: how to
// merge, what to clean up afterwards, whether to clone.
type menu struct {
	title string
	items []menuItem
}

type menuItem struct {
	label  string
	detail string
	run    func(m model) (tea.Model, tea.Cmd)
}

func (m model) openMenu(mn *menu) model {
	m.menu, m.menuCursor, m.flash = mn, 0, ""
	return m
}

func (m model) handleMenuKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.menu.items)
	switch s := k.String(); s {
	case "esc", "q":
		m.menu = nil
	case "up", "k", "ctrl+p":
		m.menuCursor = max(0, m.menuCursor-1)
	case "down", "j", "ctrl+n":
		m.menuCursor = min(n-1, m.menuCursor+1)
	case "enter":
		return m.chooseMenu(m.menuCursor)
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && int(s[0]-'1') < n {
			return m.chooseMenu(int(s[0] - '1'))
		}
	}
	return m, nil
}

func (m model) chooseMenu(i int) (tea.Model, tea.Cmd) {
	if m.menu == nil || i < 0 || i >= len(m.menu.items) {
		return m, nil
	}
	it := m.menu.items[i]
	m.menu = nil
	return it.run(m)
}

// menuTop is how many lines come before the first menu item within the
// menu's own block: the title and a blank line.
const menuTop = 2

func (m model) viewMenu(room int) string {
	var b strings.Builder
	b.WriteString(" " + styleHeader.Render(shorten(m.menu.title, max(10, m.width-2))) + "\n\n")
	for i, it := range m.menu.items {
		if i >= room-menuTop {
			break
		}
		line := fmt.Sprintf("   %s %s", styleDim.Render(fmt.Sprint(i+1)), it.label)
		if it.detail != "" {
			line += styleDim.Render("  " + it.detail)
		}
		if i == m.menuCursor {
			line = highlight(" ›"+line[2:], max(20, m.width-2))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// ── running it ────────────────────────────────────────────────────────────────

// program lets background work post progress to the UI.
var program *tea.Program

func runPicker(ctx context.Context, cfg config, demo bool) error {
	invoked := os.Getenv("HERDR_GITHUB_CWD")
	// Ask the terminal for its background now: once the program owns stdin,
	// the reply would arrive as stray input.
	if cfg.Theme != "dark" && cfg.Theme != "light" {
		cfg.Theme = "light"
		if lipgloss.HasDarkBackground() {
			cfg.Theme = "dark"
		}
	}
	useTheme(pickerTheme(cfg.Theme == "dark"))

	var client source
	var repo *repoRef
	if demo {
		d := newDemoSource()
		client, repo = d, &d.home
		spawnTickFn = func() {}
	} else {
		if invoked != "" {
			if top, err := git(ctx, invoked, "rev-parse", "--show-toplevel"); err == nil {
				if r, ok := repoOf(ctx, top); ok && cfg.knownHost(r.Host) {
					repo = &r
				}
			}
		}
		client = newGitHubSource(cfg, repo)
	}
	m := newModel(ctx, cfg, client, invoked, repo)
	if !demo {
		m = m.useCache(readListCache(repo))
	}
	// Open on the tab you were last on.
	if t, ok := lastTab(); ok && !demo {
		m.tab = t
	}
	program = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	_, err := program.Run()
	return err
}

// sortedKeys is for stable output in tests and menus.
func sortedKeys[V any](mp map[string]V) []string {
	ks := make([]string, 0, len(mp))
	for k := range mp {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func debugf(format string, args ...any) {
	p := os.Getenv("HERDR_GITHUB_DEBUG")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{nowFn().Format("15:04:05.000")}, args...)...)
}
