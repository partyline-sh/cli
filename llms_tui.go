// `ptln llms` interactive menu — a retro, full-screen session browser.
//
// Master/detail: a scrollable list of your AI CLI sessions on the left, a
// metadata + content-preview pane on the right — both in rounded panels with
// embedded titles (the classic termui / DOOR-game look), under a gradient
// switchboard banner. Arrow keys to move, `/` to search, `a` to reveal
// agent/automated sessions, Enter to drop back into one. Hand-rolled in raw
// mode (no TUI framework) — matches partyline's existing low-level terminal
// handling and keeps the binary lean.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// sessMeta is the per-session local curation state, persisted to a sidecar
// (~/.partyline/llms-meta.json) since we don't own the tools' session stores.
type sessMeta struct {
	Pinned   bool      `json:"pinned,omitempty"`
	Archived bool      `json:"archived,omitempty"`
	Name     string    `json:"name,omitempty"`      // user-given title override (for findability)
	LastUsed time.Time `json:"last_used,omitempty"` // when YOU last opened it here (survives close)
}

func llmMetaPath() string { return filepath.Join(stateDir(), "llms-meta.json") }

func loadLLMMeta() map[string]sessMeta {
	m := map[string]sessMeta{}
	if b, err := os.ReadFile(llmMetaPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// saveLLMMeta writes the sidecar, dropping empty entries so it stays tidy.
func saveLLMMeta(m map[string]sessMeta) {
	out := map[string]sessMeta{}
	for k, v := range m {
		if v.Pinned || v.Archived || v.Name != "" || !v.LastUsed.IsZero() {
			out[k] = v
		}
	}
	if b, err := json.MarshalIndent(out, "", "  "); err == nil {
		_ = os.WriteFile(llmMetaPath(), b, 0o600)
	}
}

// aiBrowse is the entry point for bare `ptln llms`. On a TTY it launches the
// persistent launcher (the mux-hosted browser); piped/redirected it degrades to
// the flat list. Resume no longer hands off via exec — sessions run in-process so
// you can drop back here and launch more (see llms_mux.go / internal/ptymux).
func aiBrowse() {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		aiList(nil)
		return
	}
	if err := runLLMSApp(nil); err != nil {
		fmt.Fprintln(os.Stderr, "ptln llms: "+err.Error())
	}
}

// Switchboard humor — picked per launch, shown under the banner.
var taglines = []string{
	"please hold — your agents are important to us",
	"☎ operator standing by",
	"your call may be recorded for context-window purposes",
	"reconnecting you now… do not hang up",
	"all sessions are answered in the order they were abandoned",
}

type sortMode int

const (
	sortRecent  sortMode = iota // last used first — the default (by last activity)
	sortOldest                  // oldest first
	sortProject                 // cluster by project, newest within each
)

func (s sortMode) label() string {
	return [...]string{"last used", "oldest", "project"}[s]
}

// treeRow is one visible line in the project tree: a project header (sessIdx < 0) or a
// session (sessIdx indexes m.view). The cursor moves over m.rows, not m.view.
type treeRow struct {
	proj    string // session Cwd (the project key); set on both headers and sessions
	sessIdx int    // index into m.view, or -1 for a project header
	count   int    // header only: number of sessions in the project
}

func (r treeRow) header() bool { return r.sessIdx < 0 }

type aiMenu struct {
	all       []aiSession
	view      []aiSession     // filtered + sorted sessions (flat); the tree is derived from this
	rows      []treeRow       // flattened project tree (headers + visible sessions); cursor indexes THIS
	collapsed map[string]bool // per-project (Cwd) collapse override: true=collapsed, false=expanded, absent=default
	cursor    int
	top       int
	showAll   bool
	sort      sortMode
	query     string
	filter    bool
	flash     string
	tagline   string
	meta      map[string]sessMeta // pin/archive sidecar, keyed by session id
	// permission-mode picker (shown on resume; see permMode)
	picking    bool
	pickModes  []permMode
	pickIdx    int
	pickArmed  bool // a danger mode is selected and awaiting a confirm press
	pickCustom bool // typing custom flags
	pickText   string
	w, h       int
	help       bool        // ? overlay open
	focusR     bool        // detail (right) pane focused for scrolling
	dTop       int         // detail scroll offset (when focusR)
	dLen       int         // last-rendered detail line count (for scroll clamping)
	fd         int         // tty fd — for suspend/resume around the diff viewer
	oldState   *term.State // cooked terminal state, restored when suspending
	// mux-hosted launcher state (see llms_mux.go). live marks sessions already running
	// in the mux; suspendFn is a pending shell-out (the diff pager) the mux runs for us.
	live      map[string]bool
	suspendFn func()
	// rename: inline edit of a session's title override (persisted to the sidecar)
	renaming  bool
	renameBuf string
	// multi-select: ids checked with space; ⏎ opens them all into the mux at once
	marked map[string]bool
	// sharing: 'S' routes the next resume through the relay host (share) instead of a
	// local mux window; spawnShell: 'n' opens a plain shell as a mux window.
	sharing    bool
	spawnShell bool
	// S2 — availability: per-project "joinable to parties" state (registry, dir-keyed) +
	// the Online/Offline presence controller that holds the daemon stream while the
	// manager is open. presence is nil when the launcher runs without a mux (piped).
	joinable map[string]daemonProject
	presence *daemonPresence
	// S3 — approval: when a launch request lands on an Online manager, a consent modal grabs
	// input ([y]/[n]/[esc]). esc snoozes it until a NEW request arrives (count exceeds the
	// snoozed level), so a reflexive esc doesn't trap you yet a fresh request re-prompts.
	apprSnooze   bool
	apprSnoozeAt int
}

// apprActive reports whether the launch-approval consent modal should own the screen + input:
// Online manager, ≥1 pending request, no other modal up, and not snoozed below a new arrival.
func (m *aiMenu) apprActive() bool {
	if m.presence == nil || m.picking || m.renaming || m.help || m.filter {
		return false
	}
	n := len(m.presence.pendingList())
	if n == 0 {
		return false
	}
	return !(m.apprSnooze && n <= m.apprSnoozeAt)
}

// reloadJoinable refreshes the dir-keyed cache of advertised projects from the registry.
func (m *aiMenu) reloadJoinable() { m.joinable = loadJoinable() }

// selected returns the session under the cursor, or nil when a project header is selected.
func (m *aiMenu) selected() *aiSession {
	if r := m.curRow(); r != nil && !r.header() {
		return &m.view[r.sessIdx]
	}
	return nil
}

// curRow is the row under the cursor (header or session), or nil if out of range.
func (m *aiMenu) curRow() *treeRow {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return &m.rows[m.cursor]
	}
	return nil
}

func projLabel(cwd string) string {
	if cwd == "" {
		return "—"
	}
	return filepath.Base(cwd)
}

// isExpanded reports whether a project's sessions are shown. A search query expands
// everything (so matches are visible); otherwise the user's explicit toggle wins; the
// default is collapsed, EXCEPT projects with a live session auto-expand.
func (m *aiMenu) isExpanded(proj string, hasLive bool) bool {
	if strings.TrimSpace(m.query) != "" {
		return true
	}
	if c, ok := m.collapsed[proj]; ok {
		return !c
	}
	return hasLive
}

// buildRows derives the flattened tree (m.rows) from m.view: group by project, project
// order = first appearance in view (so the active sort still drives ordering), sessions
// kept in view order under each header. Clamps the cursor to the new row count.
func (m *aiMenu) buildRows() {
	m.rows = m.rows[:0]
	order := make([]string, 0, 8)
	idxs := map[string][]int{}
	live := map[string]bool{}
	for i := range m.view {
		k := m.view[i].Cwd
		if _, seen := idxs[k]; !seen {
			order = append(order, k)
		}
		idxs[k] = append(idxs[k], i)
		if m.live[m.view[i].ID] {
			live[k] = true
		}
	}
	for _, k := range order {
		ix := idxs[k]
		m.rows = append(m.rows, treeRow{proj: k, sessIdx: -1, count: len(ix)})
		if m.isExpanded(k, live[k]) {
			for _, i := range ix {
				m.rows = append(m.rows, treeRow{proj: k, sessIdx: i})
			}
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// selectProjHeader puts the cursor on a project's header row.
func (m *aiMenu) selectProjHeader(proj string) {
	for i := range m.rows {
		if m.rows[i].header() && m.rows[i].proj == proj {
			m.cursor = i
			return
		}
	}
}

// setCollapsed sets a project's collapse state, rebuilds, and keeps the cursor on its header.
func (m *aiMenu) setCollapsed(proj string, collapsed bool) {
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	m.collapsed[proj] = collapsed
	m.buildRows()
	m.selectProjHeader(proj)
	m.clampScroll()
}

// toggleCollapse flips the collapse state of the header under the cursor.
func (m *aiMenu) toggleCollapse() {
	r := m.curRow()
	if r == nil || !r.header() {
		return
	}
	hasLive := false
	for i := range m.view {
		if m.view[i].Cwd == r.proj && m.live[m.view[i].ID] {
			hasLive = true
			break
		}
	}
	m.setCollapsed(r.proj, m.isExpanded(r.proj, hasLive)) // currently expanded → collapse, and vice-versa
}

// displayTitle is the user's custom name override if set, else the tool-derived title.
func (m *aiMenu) displayTitle(s aiSession) string {
	if n := strings.TrimSpace(m.meta[s.ID].Name); n != "" {
		return n
	}
	return s.Title
}

// commitRename persists the rename buffer to the highlighted session's sidecar entry.
// An empty buffer clears the override (falls back to the tool-derived title).
func (m *aiMenu) commitRename() {
	s := m.selected()
	m.renaming = false
	if s == nil {
		m.renameBuf = ""
		return
	}
	if m.meta == nil {
		m.meta = map[string]sessMeta{}
	}
	mt := m.meta[s.ID]
	mt.Name = strings.TrimSpace(m.renameBuf)
	m.meta[s.ID] = mt
	saveLLMMeta(m.meta)
	m.renameBuf = ""
	if mt.Name == "" {
		m.flash = "✓ name cleared"
	} else {
		m.flash = "✓ renamed"
	}
	m.applyFilter()
}

// permMode is one entry in the resume permission picker. flags are appended
// verbatim to the tool's native resume command — we don't invent a cross-tool
// abstraction (codex's approval×sandbox doesn't collapse to one), we surface
// each tool's real modes. danger = "runs without asking" (needs a confirm).
type permMode struct {
	label  string
	flags  []string
	danger bool
	custom bool // the "custom flags…" entry → opens free-text entry
}

// permissionModes returns the curated presets for a tool's resume, or nil for
// tools with no permission concept (llm). The first entry is always the
// safe default (no flags). A "custom flags…" escape hatch future-proofs new
// modes without a CLI update (same philosophy as `party`'s `--` passthrough).
func permissionModes(tool string) []permMode {
	switch tool {
	case "claude":
		return []permMode{
			{label: "default"},
			{label: "accept-edits", flags: []string{"--permission-mode", "acceptEdits"}},
			{label: "plan", flags: []string{"--permission-mode", "plan"}},
			{label: "bypass", flags: []string{"--permission-mode", "bypassPermissions"}, danger: true},
			{label: "custom…", custom: true},
		}
	case "gemini":
		return []permMode{
			{label: "default"},
			{label: "auto-edit", flags: []string{"--approval-mode", "auto_edit"}},
			{label: "plan", flags: []string{"--approval-mode", "plan"}},
			{label: "yolo", flags: []string{"--approval-mode", "yolo"}, danger: true},
			{label: "custom…", custom: true},
		}
	case "codex":
		return []permMode{
			{label: "default"},
			{label: "full-auto", flags: []string{"--full-auto"}},
			{label: "bypass", flags: []string{"--dangerously-bypass-approvals-and-sandbox"}, danger: true},
			{label: "custom…", custom: true},
		}
	}
	return nil
}

func toolColor(tool string) int {
	switch tool {
	case "claude":
		return 215 // orange
	case "codex":
		return 80 // cyan
	case "gemini":
		return 75 // blue
	case "llm":
		return 207 // magenta
	default:
		return 245
	}
}

func (m *aiMenu) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.query))
	m.view = m.view[:0]
	for _, s := range m.all {
		mt := m.meta[s.ID]
		// `a` (showAll) reveals BOTH agent-spawned and archived sessions.
		if !m.showAll && (isAgentSession(s) || mt.Archived) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(s.Tool + " " + filepath.Base(s.Cwd) + " " + s.Title + " " + m.meta[s.ID].Name + " " + s.ID)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		m.view = append(m.view, s)
	}
	// Pinned always float to the top; then (in recent sort) sessions currently open in
	// the mux — your active working set — so opening one surfaces it; then the sort order.
	sort.SliceStable(m.view, func(i, j int) bool {
		a, b := m.view[i], m.view[j]
		if pa, pb := m.meta[a.ID].Pinned, m.meta[b.ID].Pinned; pa != pb {
			return pa
		}
		if m.sort == sortRecent {
			if la, lb := m.live[a.ID], m.live[b.ID]; la != lb {
				return la
			}
		}
		switch m.sort {
		case sortOldest:
			return a.LastActive.Before(b.LastActive)
		case sortProject:
			ba, bb := filepath.Base(a.Cwd), filepath.Base(b.Cwd)
			if ba != bb {
				return ba < bb
			}
			return a.LastActive.After(b.LastActive)
		default: // sortRecent — "last used": newest of (activity, when you last opened it here)
			return m.lastUsed(a).After(m.lastUsed(b))
		}
	})
	m.buildRows() // rebuild the project tree from the new view (also clamps the cursor)
	m.clampScroll()
}

// bodyH is the panel-interior height: banner(2) + borders(2) + footer(1).
// refresh re-scans the session stores so newly-started (or ended) sessions appear and
// live status updates. Keeps the highlighted session selected by id across the rebuild,
// and drops cached detail so the right pane + status reflect reality.
func (m *aiMenu) refresh() {
	cur := ""
	if s := m.selected(); s != nil {
		cur = s.ID
	}
	m.all = collectSessions()
	detailCache = map[string]*aiDetail{}
	m.applyFilter()
	if cur != "" {
		m.selectByID(cur)
	}
}

// lastUsed is the sort key for the default "last used" order: the more recent of the
// session's own activity and the last time you opened it in this launcher (persisted in
// the sidecar, so an opened-then-closed session stays near the top).
func (m *aiMenu) lastUsed(s aiSession) time.Time {
	if lu := m.meta[s.ID].LastUsed; lu.After(s.LastActive) {
		return lu
	}
	return s.LastActive
}

// markUsed records that you just opened a session here (now), so "last used" sort keeps it
// near the top even after it's closed.
func (m *aiMenu) markUsed(id string) {
	if m.meta == nil {
		m.meta = map[string]sessMeta{}
	}
	mt := m.meta[id]
	mt.LastUsed = time.Now()
	m.meta[id] = mt
	saveLLMMeta(m.meta)
}

// selectByID moves the cursor to the session with the given id if it's in the current
// view (used to keep the highlight stable across a re-sort/refilter). No-op otherwise.
func (m *aiMenu) selectByID(id string) {
	proj, found := "", false
	for i := range m.view {
		if m.view[i].ID == id {
			proj, found = m.view[i].Cwd, true
			break
		}
	}
	if !found {
		return
	}
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	m.collapsed[proj] = false // ensure the project is expanded so its session is visible
	m.buildRows()
	for i := range m.rows {
		if !m.rows[i].header() && m.view[m.rows[i].sessIdx].ID == id {
			m.cursor = i
			m.clampScroll()
			return
		}
	}
}

func (m *aiMenu) bodyH() int { return m.h - 5 }

func (m *aiMenu) clampScroll() {
	bh := m.bodyH()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+bh {
		m.top = m.cursor - bh + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

// handleKey returns (done, chosen). done=true ends the loop; chosen!=nil resumes.
func (m *aiMenu) handleKey(b []byte) (bool, *aiSession) {
	if len(b) == 0 { // e.g. an input chunk that was entirely a dropped key-release event
		return false, nil
	}
	m.flash = ""
	if m.picking {
		return m.handlePicker(b)
	}
	if m.help { // any key closes the help overlay
		m.help = false
		return false, nil
	}
	if m.apprActive() { // a launch-approval consent modal is up — it owns input
		return m.handleApproval(b)
	}
	// Escape sequences (arrows, page up/down). When the detail pane is focused,
	// up/down scroll it instead of moving the list cursor; →/← focus/unfocus it.
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			if m.focusR {
				m.scrollDetail(-1)
			} else {
				m.move(-1)
			}
		case 'B':
			if m.focusR {
				m.scrollDetail(1)
			} else {
				m.move(1)
			}
		case '5':
			if m.focusR {
				m.scrollDetail(-m.bodyH())
			} else {
				m.move(-m.bodyH())
			}
		case '6':
			if m.focusR {
				m.scrollDetail(m.bodyH())
			} else {
				m.move(m.bodyH())
			}
		case 'C': // →: expand a project header; on a session, focus the detail pane
			if r := m.curRow(); r != nil && r.header() {
				m.setCollapsed(r.proj, false)
			} else {
				m.focusR = true
			}
		case 'D': // ←: collapse a header; on a session, unfocus detail or jump to its header
			if r := m.curRow(); r != nil && r.header() {
				m.setCollapsed(r.proj, true)
			} else if m.focusR {
				m.focusR, m.dTop = false, 0
			} else if r != nil {
				m.selectProjHeader(r.proj)
				m.clampScroll()
			}
		}
		return false, nil
	}
	// Any other ESC-led input (bare ESC, or ESC coalesced with a following byte
	// by a fast terminal) is treated as ESC — the arrow case above already
	// returned, so nothing here is a recognized sequence.
	if b[0] == 0x1b {
		if m.renaming {
			m.renaming, m.renameBuf = false, ""
		} else if m.filter {
			m.filter, m.query = false, ""
			m.applyFilter()
		} else if len(m.marked) > 0 {
			m.marked = nil // esc clears a multi-select
		}
		return false, nil
	}
	c := b[0]
	if m.renaming {
		switch {
		case c == '\r' || c == '\n':
			m.commitRename()
		case c == 0x7f || c == 0x08: // backspace
			if m.renameBuf != "" {
				m.renameBuf = m.renameBuf[:len(m.renameBuf)-1]
			}
		case c >= 0x20 && c < 0x7f:
			m.renameBuf += string(rune(c))
		}
		return false, nil
	}
	if m.filter {
		switch {
		case c == '\r' || c == '\n':
			m.filter = false
		case c == 0x7f || c == 0x08: // backspace
			if m.query != "" {
				m.query = m.query[:len(m.query)-1]
			}
			m.applyFilter()
		case c >= 0x20 && c < 0x7f:
			m.query += string(rune(c))
			m.applyFilter()
		}
		return false, nil
	}
	switch c {
	case 'q', 0x03: // q or ctrl-c
		return true, nil
	case 'j':
		m.move(1)
	case 'k':
		m.move(-1)
	case 'g':
		m.cursor = 0
		m.clampScroll()
	case 'G':
		m.cursor = len(m.rows) - 1
		m.clampScroll()
	case '/':
		m.filter = true
	case 'r': // rename: edit this session's title override (for findability)
		if s := m.selected(); s != nil {
			m.renaming, m.renameBuf = true, strings.TrimSpace(m.meta[s.ID].Name)
		}
	case 'a':
		m.showAll = !m.showAll
		m.applyFilter()
	case 's': // cycle sort: recent → oldest → project
		m.sort = (m.sort + 1) % 3
		m.applyFilter()
		m.flash = "✓ sort: " + m.sort.label()
	case 'R': // refresh: re-scan stores so newly-started sessions show up
		m.refresh()
		m.flash = "✓ refreshed"
	case ' ': // select/deselect this session for opening several at once (⏎ opens marked)
		if s := m.selected(); s != nil {
			if m.marked == nil {
				m.marked = map[string]bool{}
			}
			if m.marked[s.ID] {
				delete(m.marked, s.ID)
			} else {
				m.marked[s.ID] = true
			}
			m.move(1) // advance so you can rattle through with space
		}
	case 'P': // availability: cycle this project joinable→Ask→Auto→off (acts on the header's project)
		if r := m.curRow(); r != nil {
			_, flash := cycleJoinable(r.proj)
			m.reloadJoinable()
			m.flash = flash
			if m.presence != nil {
				m.presence.joinableChanged()
			}
		}
	case 'O': // presence: toggle Online/Offline (manager-open = daemon)
		if m.presence != nil {
			m.flash = m.presence.toggle()
		} else {
			m.flash = "presence unavailable in this mode"
		}
	case 'p', 'x': // pin / archive the highlighted session (persisted; no-op on a header)
		if s := m.selected(); s != nil {
			id := s.ID
			mt := m.meta[id]
			if c == 'p' {
				mt.Pinned = !mt.Pinned
				m.flash = map[bool]string{true: "✓ ★ pinned", false: "✓ unpinned"}[mt.Pinned]
			} else {
				mt.Archived = !mt.Archived
				m.flash = map[bool]string{true: "✓ archived · a shows it, x un-archives", false: "✓ unarchived"}[mt.Archived]
			}
			if m.meta == nil {
				m.meta = map[string]sessMeta{}
			}
			m.meta[id] = mt
			saveLLMMeta(m.meta)
			m.applyFilter()
		}
	case 'o': // open in a new terminal tab; the menu stays up
		if s := m.selected(); s != nil {
			if s.resumeArgv == nil {
				m.flash = s.Tool + " sessions are list-only (no recorded path to resume into)"
				return false, nil
			}
			if msg, err := openInNewTab(*s); err != nil {
				m.flash = err.Error()
			} else {
				m.flash = "✓ " + msg
			}
		}
	case '\t': // toggle focus between the list and the (scrollable) detail pane
		m.focusR = !m.focusR
		if !m.focusR {
			m.dTop = 0
		}
	case 'l': // focus the detail pane
		m.focusR = true
	case 'h': // back to the list
		m.focusR, m.dTop = false, 0
	case 'd': // view this session's repo diff in a pager (read-only)
		if s := m.selected(); s != nil {
			if fn := m.diffClosure(*s); fn != nil {
				m.suspendFn = fn // the mux suspends the terminal and runs it
			}
		}
	case 't': // cycle the colour theme (persisted) — for dark vs light terminals
		p := cycleTheme()
		m.flash = "✓ theme: " + p.name + " — " + p.forBg
	case 'n': // open a plain shell as a new mux window (terminals + sessions in one tab)
		m.spawnShell = true
	case 'S': // share: host this session over the relay (view-only) so someone can join
		s := m.selected()
		if s == nil {
			return false, nil
		}
		if s.resumeArgv == nil {
			m.flash = s.Tool + " sessions are list-only — can't share"
			return false, nil
		}
		m.sharing = true
		modes := permissionModes(s.Tool)
		if modes == nil { // no permission concept → share straight away
			return true, s
		}
		m.picking, m.pickModes, m.pickIdx = true, modes, 0
		m.pickArmed, m.pickCustom, m.pickText = false, false, ""
	case 'D': // open the partyline docs in your browser
		openBrowser("https://partyline.sh/docs")
		m.flash = "✓ opening partyline.sh/docs in your browser"
	case '?':
		m.help = true
	case '\r', '\n':
		if r := m.curRow(); r != nil && r.header() { // ⏎ on a project header toggles it
			m.toggleCollapse()
			return false, nil
		}
		if s := m.selected(); s != nil {
			if s.resumeArgv == nil {
				m.flash = s.Tool + " sessions are list-only (no recorded path to resume into)"
				return false, nil
			}
			modes := permissionModes(s.Tool)
			if modes == nil { // no permission concept (llm) → resume straight away
				cp := *s
				return true, &cp
			}
			// Open the permission picker (default pre-selected — safest).
			m.picking, m.pickModes, m.pickIdx = true, modes, 0
			m.pickArmed, m.pickCustom, m.pickText = false, false, ""
		}
	}
	return false, nil
}

// handleApproval drives the launch-approval consent modal. [y]/⏎ approve the oldest pending
// request (authorize → the daemon executes on the accepted event), [n]/[d] decline it, bare
// esc snoozes the prompt. Arrow/CSI sequences are swallowed so they don't trip the esc case.
func (m *aiMenu) handleApproval(b []byte) (bool, *aiSession) {
	pend := m.presence.pendingList()
	if len(pend) == 0 {
		return false, nil
	}
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		return false, nil // arrow/CSI — ignore while the prompt is up
	}
	cur := pend[0]
	switch c := b[0]; {
	case c == 0x1b: // bare esc → later (re-prompts when a new request arrives)
		m.apprSnooze, m.apprSnoozeAt = true, len(pend)
	case c == 'y' || c == 'Y' || c == '\r' || c == '\n':
		m.presence.approve(cur.reqID, cur.label)
		m.apprSnooze = false
		m.flash = "✓ approved — launching " + cur.label
	case c == 'n' || c == 'N' || c == 'd':
		m.presence.deny(cur.reqID)
		m.apprSnooze = false
		m.flash = "✓ declined " + cur.label
	}
	return false, nil
}

// handlePicker drives the resume permission-mode picker. Returns (done, chosen):
// chosen carries the session with the picked flags appended to its resume argv.
func (m *aiMenu) handlePicker(b []byte) (bool, *aiSession) {
	// In custom free-text entry.
	if m.pickCustom {
		c := b[0]
		switch {
		case b[0] == 0x1b: // esc → back to the preset list
			m.pickCustom = false
		case c == '\r' || c == '\n':
			m.picking, m.pickCustom = false, false
			return true, m.resumeWith(strings.Fields(m.pickText))
		case c == 0x7f || c == 0x08:
			if m.pickText != "" {
				m.pickText = m.pickText[:len(m.pickText)-1]
			}
		case c >= 0x20 && c < 0x7f:
			m.pickText += string(rune(c))
		}
		return false, nil
	}
	// Mode list (vertical modal): ↑↓ (or ←→ / h/l) move, ⏎ choose, esc/q cancel.
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'B', 'C': // ↓ / →
			m.pickIdx = (m.pickIdx + 1) % len(m.pickModes)
			m.pickArmed = false
		case 'A', 'D': // ↑ / ←
			m.pickIdx = (m.pickIdx - 1 + len(m.pickModes)) % len(m.pickModes)
			m.pickArmed = false
		}
		return false, nil
	}
	if b[0] == 0x1b { // bare esc → cancel
		m.picking, m.pickArmed, m.sharing = false, false, false
		return false, nil
	}
	switch b[0] {
	case 'q':
		m.picking, m.pickArmed, m.sharing = false, false, false
	case 'l':
		m.pickIdx = (m.pickIdx + 1) % len(m.pickModes)
		m.pickArmed = false
	case 'h':
		m.pickIdx = (m.pickIdx - 1 + len(m.pickModes)) % len(m.pickModes)
		m.pickArmed = false
	case '\r', '\n':
		mode := m.pickModes[m.pickIdx]
		switch {
		case mode.custom:
			m.pickCustom, m.pickText = true, ""
		case mode.danger && !m.pickArmed:
			m.pickArmed = true // require a second ⏎ to confirm a "runs without asking" mode
		default:
			m.picking, m.pickArmed = false, false
			return true, m.resumeWith(mode.flags)
		}
	}
	return false, nil
}

// resumeWith returns a copy of the highlighted session with extra flags appended
// to its resume command (fresh slice — never mutate the cached session's argv).
func (m *aiMenu) resumeWith(flags []string) *aiSession {
	s := m.selected()
	if s == nil {
		return nil
	}
	cp := *s
	cp.resumeArgv = append(append([]string{}, s.resumeArgv...), flags...)
	return &cp
}

func (m *aiMenu) move(d int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += d
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	m.dTop = 0 // new selection → reset the detail scroll
	m.clampScroll()
}

// scrollDetail scrolls the (focused) detail pane, clamped to its content. dLen is
// set during render so we always clamp against what's actually shown.
func (m *aiMenu) scrollDetail(d int) {
	m.dTop += d
	maxTop := m.dLen - m.bodyH()
	if maxTop < 0 {
		maxTop = 0
	}
	if m.dTop > maxTop {
		m.dTop = maxTop
	}
	if m.dTop < 0 {
		m.dTop = 0
	}
}

// ---- render ----------------------------------------------------------------

const frameClr = "\x1b[38;5;238m" // panel borders
const reset = "\x1b[0m"

// banner gradient ramp — warm orange → pink → magenta, lolcat-ish.
var ramp = []int{208, 209, 214, 215, 220, 215, 213, 212, 211, 205, 204, 203}

func gradient(s string) string {
	var b strings.Builder
	i := 0
	for _, r := range s {
		if r != ' ' {
			fmt.Fprintf(&b, "\x1b[1;38;5;%dm%c", ramp[i%len(ramp)], r)
			i++
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteString(reset)
	return b.String()
}

func (m *aiMenu) render() {
	if m.help {
		m.renderHelp()
		return
	}
	listW := m.w * 52 / 100
	if listW < 32 {
		listW = 32
	}
	if listW > m.w-28 {
		listW = m.w - 28
	}
	detailW := m.w - listW - 1 // 1-col gap between panels
	bh := m.bodyH()

	left := m.listLines(listW-4, bh) // panel interior = width − "│ " − " │"
	right := m.detailLines(detailW - 4)
	// Clamp the detail scroll offset to the content actually rendered this frame.
	m.dLen = len(right)
	maxTop := m.dLen - bh
	if maxTop < 0 {
		maxTop = 0
	}
	if m.dTop > maxTop {
		m.dTop = maxTop
	}
	if m.dTop < 0 {
		m.dTop = 0
	}

	var sb strings.Builder
	sb.WriteString("\x1b[H")

	// Banner: gradient wordmark + count, then the tagline.
	hidden := m.hiddenCount()
	projs := 0
	for i := range m.rows {
		if m.rows[i].header() {
			projs++
		}
	}
	counts := fmt.Sprintf("%d projects · %d sessions", projs, len(m.view))
	if hidden > 0 && !m.showAll {
		counts += fmt.Sprintf(" · %d agents on hold", hidden)
	}
	title := gradient("☎ P A R T Y L I N E") + "  \x1b[38;5;245m· session switchboard · " + counts + reset
	sb.WriteString(padAnsi(" "+title, m.w) + "\x1b[K\r\n")
	// Row 2: the tagline (left) + the daemon presence status (right-aligned).
	left2 := "   \x1b[3;38;5;240m" + m.tagline + reset
	status := m.presenceStatus()
	gap := m.w - visWidth(left2) - visWidth(status) - 1
	if gap < 1 {
		gap = 1
	}
	sb.WriteString(clip(left2+strings.Repeat(" ", gap)+status, m.w) + "\x1b[K\r\n")

	// Panel tops with embedded titles (the left title carries the sort mode).
	lTitle := " sessions · " + m.sort.label() + " "
	if m.query != "" {
		lTitle = " sessions /" + m.query + " "
	}
	rTitle := " detail "
	if s := m.selected(); s != nil {
		rTitle = " " + s.Tool + " "
	}
	rColor := 245
	if m.focusR { // focused: brighten the title + hint that ↑↓ scroll
		rColor = 215
		rTitle = strings.TrimRight(rTitle, " ") + " ▾ scroll "
	}
	sb.WriteString(boxTop(lTitle, listW, 111) + " " + boxTop(rTitle, detailW, rColor) + "\x1b[K\r\n")

	// Body rows: interior lines wrapped in panel borders.
	for i := 0; i < bh; i++ {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		r := ""
		if idx := i + m.dTop; idx < len(right) {
			r = right[idx]
		}
		sb.WriteString(frameClr + "│" + reset + " " + padAnsi(l, listW-4) + " " + frameClr + "│" + reset)
		sb.WriteString(" ")
		sb.WriteString(frameClr + "│" + reset + " " + padAnsi(r, detailW-4) + " " + frameClr + "│" + reset + "\x1b[K\r\n")
	}
	sb.WriteString(boxBottom(listW) + " " + boxBottom(detailW) + "\x1b[K\r\n")
	sb.WriteString(padAnsi(m.footer(), m.w) + "\x1b[K")
	sb.WriteString("\x1b[J")
	if m.picking { // overlay the resume modal on top of the list
		m.appendPickerModal(&sb)
	}
	if m.renaming { // overlay the rename modal on top of the list
		m.appendRenameModal(&sb)
	}
	if m.apprActive() { // overlay the launch-approval consent modal (highest priority)
		m.appendApprovalModal(&sb)
	}
	os.Stdout.WriteString(themed(sb.String())) // re-skin the whole frame to the active theme
}

// boxTop draws "╭─┤ title ├──…──╮" with the title tinted by color c.
func boxTop(title string, w, c int) string {
	inner := w - 2 // between the corners
	t := fmt.Sprintf("┤\x1b[1;38;5;%dm%s\x1b[0m%s├", c, title, frameClr)
	tlen := len([]rune(title)) + 2
	if tlen > inner-2 {
		return frameClr + "╭" + strings.Repeat("─", max(0, inner)) + "╮" + reset
	}
	return frameClr + "╭─" + t + strings.Repeat("─", inner-tlen-1) + "╮" + reset
}

func boxBottom(w int) string {
	return frameClr + "╰" + strings.Repeat("─", max(0, w-2)) + "╯" + reset
}

func (m *aiMenu) listLines(w, bh int) []string {
	if len(m.view) == 0 {
		return asciiShrug(w, "nothing on the line")
	}
	out := make([]string, 0, bh)
	for i := 0; i < bh; i++ {
		idx := m.top + i
		if idx >= len(m.rows) {
			out = append(out, "")
			continue
		}
		r := m.rows[idx]
		sel := idx == m.cursor
		var row string
		if r.header() {
			row = m.headerRow(r, sel)
		} else {
			row = m.sessionRow(&m.view[r.sessIdx], sel)
		}
		if sel {
			out = append(out, "\x1b[48;5;236m"+padAnsi(row, w)+reset)
		} else {
			out = append(out, clip(row, w))
		}
	}
	return out
}

// headerRow renders a project header: expand glyph (▸/▾), an aggregate status dot
// (green = any live/active · amber ⏳ = any waiting), the project label, and the count.
func (m *aiMenu) headerRow(r treeRow, sel bool) string {
	fg := func(c int) string { return fmt.Sprintf("\x1b[38;5;%dm", c) }
	anyActive, anyWaiting, hasLive := false, false, false
	for j := range m.view {
		s := m.view[j]
		if s.Cwd != r.proj {
			continue
		}
		if m.live[s.ID] {
			hasLive = true
		}
		switch s.Status {
		case "active":
			anyActive = true
		case "waiting":
			anyWaiting = true
		}
	}
	glyph := "▸"
	if m.isExpanded(r.proj, hasLive) {
		glyph = "▾"
	}
	dot, dotClr := "●", 240
	if anyActive || hasLive {
		dotClr = 46
	} else if anyWaiting {
		dot, dotClr = "⏳", 214
	}
	nameClr := fg(252)
	if sel {
		nameClr = "\x1b[1m\x1b[38;5;231m"
	}
	badge := ""
	if pr, ok := m.joinable[r.proj]; ok { // advertised to parties — show policy
		if pr.launchPolicy() == "auto" {
			badge = fg(46) + "  ⚡join·Auto"
		} else {
			badge = fg(75) + "  ⚡join·Ask"
		}
	}
	return fg(244) + glyph + " " + fg(dotClr) + dot + " " + nameClr + projLabel(r.proj) +
		fg(240) + fmt.Sprintf(" (%d)", r.count) + badge
}

// sessionRow renders one session, indented under its project header. The project column is
// dropped (the header carries it), leaving more room for the title.
func (m *aiMenu) sessionRow(s *aiSession, sel bool) string {
	// fg-only color codes (no resets inside) so a selection background can wrap the line.
	fg := func(c int) string { return fmt.Sprintf("\x1b[38;5;%dm", c) }
	titleClr, marker := fg(252), "    " // indent under the header
	if sel {
		titleClr, marker = "\x1b[1m\x1b[38;5;231m", "  "+fg(215)+"▸\x1b[22m " // bold arrow; 22 undoes bold
	}
	dotClr := toolColor(s.Tool)
	switch s.Status {
	case "waiting":
		dotClr = 214 // amber: the agent is waiting on YOU
	case "active":
		dotClr = 46 // bright green: running right now
	}
	live := m.live[s.ID]
	if live {
		dotClr = 46 // open in this mux → ⏎ jumps to the window
	}
	glyph := "●"
	if m.marked[s.ID] {
		glyph, dotClr = "✓", 51 // marked for multi-open: cyan ✓
	}
	title := m.displayTitle(*s)
	if m.meta[s.ID].Pinned {
		title = fg(220) + "★ " + titleClr + m.displayTitle(*s)
	}
	if live {
		title += fg(46) + " ▸live"
	}
	return marker + fg(dotClr) + glyph + " " + fg(242) + fmt.Sprintf("%4s ", humanAge(s.LastActive)) +
		fg(toolColor(s.Tool)) + fmt.Sprintf("%-6s ", s.Tool) + titleClr + title
}

func (m *aiMenu) detailLines(w int) []string {
	if len(m.view) == 0 {
		return asciiShrug(w, "no sessions match")
	}
	sel := m.selected()
	if sel == nil { // a project header is highlighted — nothing to preview
		if r := m.curRow(); r != nil {
			return asciiShrug(w, projLabel(r.proj)+" · ⏎ or → to expand")
		}
		return asciiShrug(w, "select a session")
	}
	s := *sel
	d := detailFor(s)
	var ls []string
	add := func(s string) { ls = append(ls, clip(s, w)) }
	label := func(k, v string) {
		if v == "" {
			v = "—"
		}
		add(fmt.Sprintf("\x1b[38;5;243m%-9s\x1b[0m%s", k, v))
	}

	live := ""
	switch s.Status {
	case "waiting":
		live = "  \x1b[1;38;5;214m⏳ waiting for you\x1b[0m"
	case "active":
		live = "  \x1b[1;38;5;46m● running\x1b[0m"
	}
	add(fmt.Sprintf("\x1b[1;38;5;%dm%s\x1b[0m \x1b[38;5;245m%s\x1b[0m%s", toolColor(s.Tool), strings.ToUpper(s.Tool), short(s.ID, 20), live))
	add("")
	loc := s.Cwd
	if loc == "" {
		loc = "(no path recorded)"
	}
	label("location", loc)
	ctx := ""
	if d.Gone {
		ctx = "\x1b[38;5;203m✗ gone — directory no longer exists\x1b[0m"
	} else if d.Branch != "" {
		ctx = "\x1b[38;5;114m⎇ " + d.Branch + "\x1b[0m"
	} else if s.Cwd != "" {
		ctx = "\x1b[38;5;114m✓ exists\x1b[0m"
	}
	// Uncommitted-changes flag — "is this session's work saved?"
	if d.Dirty > 0 {
		ctx += fmt.Sprintf("  \x1b[38;5;215m⚠ %d uncommitted\x1b[0m", d.Dirty)
	} else if d.Dirty == 0 && !d.Gone {
		ctx += "  \x1b[38;5;108m✓ clean\x1b[0m"
	}
	if ctx != "" {
		label("", ctx)
	}
	if !d.Started.IsZero() {
		label("started", d.Started.Format("2006-01-02 15:04"))
	}
	// "span" = first→last activity (a session resumed over days spans days); it's
	// not active compute time, so don't call it "duration".
	if dur := d.Ended.Sub(d.Started); dur >= time.Minute && !d.Started.IsZero() && !d.Ended.IsZero() {
		label("span", humanDur(dur))
	}
	label("active", humanAge(s.LastActive)+" ago")
	label("model", d.Model)
	if d.Tokens > 0 {
		tok := humanTokens(d.Tokens)
		if d.TokensPartial {
			tok = "~" + tok + " (partial)"
		}
		label("tokens", tok)
	}
	if d.Messages > 0 {
		label("messages", fmt.Sprintf("%d", d.Messages))
	} else if d.Size > 0 {
		label("size", humanSize(d.Size))
	}
	add("\x1b[38;5;237m" + strings.Repeat("╌", max(0, w)) + reset)
	add("\x1b[1;38;5;243mfirst prompt\x1b[0m")
	for _, line := range wrap(d.First, w-2, 4) {
		add("  \x1b[38;5;250m" + line + reset)
	}
	// Recent conversation tail: the last few messages, role-labelled. The pane scrolls
	// (→/tab to focus, ↑↓), so this can run long. Falls back to the single latest message
	// for stores we can't break into a tail.
	if len(d.Recent) > 0 {
		add("")
		add("\x1b[1;38;5;243mrecent\x1b[0m")
		for _, rm := range d.Recent {
			label, clr := rm.Role, 245
			switch rm.Role {
			case "user":
				label, clr = "you", 117 // blue
			case "assistant", "model":
				label, clr = "agent", 215 // orange
			case "tool", "tool_use", "tool_result":
				label, clr = "tool", 108 // green
			}
			add(fmt.Sprintf("\x1b[1;38;5;%dm%s\x1b[0m", clr, label))
			for _, line := range wrap(rm.Text, w-2, 6) {
				add("  \x1b[38;5;250m" + line + reset)
			}
		}
	} else if d.Last != "" && d.Last != d.First {
		add("")
		add("\x1b[1;38;5;243mlatest\x1b[0m")
		for _, line := range wrap(d.Last, w-2, 4) {
			add("  \x1b[38;5;250m" + line + reset)
		}
	}
	// Context: what this session runs *with* — its memory file, MCP servers, skills.
	if len(d.Memory) > 0 || len(d.MCP) > 0 || len(d.Skills) > 0 {
		add("\x1b[38;5;237m" + strings.Repeat("╌", max(0, w)) + reset)
		add("\x1b[1;38;5;243mcontext\x1b[0m")
		for _, mf := range d.Memory {
			add(fmt.Sprintf("  \x1b[38;5;250m%s\x1b[0m \x1b[38;5;243m· %d lines · %s\x1b[0m", filepath.Base(mf.Path), mf.Lines, mf.Scope))
		}
		if len(d.MCP) > 0 {
			add("  \x1b[38;5;243mmcp\x1b[0m")
			for _, ln := range wrap(strings.Join(d.MCP, ", "), w-2, 4) {
				add("    \x1b[38;5;250m" + ln + reset)
			}
		}
		if len(d.Skills) > 0 {
			add(fmt.Sprintf("  \x1b[38;5;243mskills\x1b[0m \x1b[38;5;243m(%d)\x1b[0m", len(d.Skills)))
			for _, ln := range wrap(strings.Join(d.Skills, ", "), w-2, 4) {
				add("    \x1b[38;5;250m" + ln + reset)
			}
		}
	}
	return ls
}

// appendPickerModal overlays the resume permission picker as a centered modal box (drawn
// on top of the list with absolute cursor positioning). The footer keeps a short hint.
func (m *aiMenu) appendPickerModal(sb *strings.Builder) {
	tool := ""
	if s := m.selected(); s != nil {
		tool = s.Tool
	}
	title := " resume " + tool + " "

	var lines []string
	if m.pickCustom {
		lines = []string{
			"\x1b[38;5;245mcustom flags:\x1b[0m",
			"  \x1b[38;5;231m" + m.pickText + "\x1b[7m \x1b[0m",
			"",
			"\x1b[38;5;240m⏎ run · esc back\x1b[0m",
		}
	} else {
		for i, md := range m.pickModes {
			marker, lbl, clr := "  ", md.label, "\x1b[38;5;250m"
			if md.danger {
				lbl, clr = "⚠ "+lbl, "\x1b[38;5;203m"
			}
			if i == m.pickIdx {
				marker = "\x1b[38;5;215m▸\x1b[0m "
				if md.danger {
					clr = "\x1b[1;38;5;203m"
				} else {
					clr = "\x1b[1;38;5;231m"
				}
			}
			lines = append(lines, marker+clr+lbl+reset)
		}
		lines = append(lines, "")
		tail := "\x1b[38;5;240m↑↓ pick · ⏎ go · esc cancel\x1b[0m"
		if m.pickArmed {
			tail = "\x1b[1;38;5;203m⚠ ⏎ again to confirm\x1b[0m"
		}
		lines = append(lines, tail)
	}
	m.appendModalBox(sb, title, lines)
}

// appendApprovalModal overlays the launch-approval consent prompt: the oldest pending request
// (project + preset), what it'll run as, and the y/n/esc keys. The web modal carries the full
// who/party detail; this CLI banner is the project-focused "someone wants to launch here" gate.
func (m *aiMenu) appendApprovalModal(sb *strings.Builder) {
	pend := m.presence.pendingList()
	if len(pend) == 0 {
		return
	}
	cur := pend[0]
	lines := []string{
		"\x1b[1;38;5;231m" + clip(cur.label, 40) + "\x1b[0m \x1b[38;5;245m· " + cur.preset + "\x1b[0m",
		"\x1b[38;5;245ma teammate wants to start a read-only\x1b[0m",
		"\x1b[38;5;245mgrounded agent here\x1b[0m",
		"",
		"\x1b[1;38;5;46m[y] approve\x1b[0m   \x1b[1;38;5;203m[n] decline\x1b[0m   \x1b[38;5;240m[esc] later\x1b[0m",
	}
	if len(pend) > 1 {
		lines = append(lines, "", fmt.Sprintf("\x1b[38;5;240m+%d more waiting\x1b[0m", len(pend)-1))
	}
	m.appendModalBox(sb, " ⚡ launch request ", lines)
}

// appendRenameModal overlays the session-rename editor as a centered modal.
func (m *aiMenu) appendRenameModal(sb *strings.Builder) {
	was := ""
	if s := m.selected(); s != nil {
		was = s.Title
	}
	lines := []string{
		"\x1b[38;5;245mwas:\x1b[0m " + clip(was, 46),
		"",
		"\x1b[1;38;5;231m" + m.renameBuf + "\x1b[7m \x1b[0m",
		"",
		"\x1b[38;5;240m⏎ save · empty resets · esc cancel\x1b[0m",
	}
	m.appendModalBox(sb, " rename session ", lines)
}

// appendModalBox draws a centered rounded modal box (embedded title, pre-styled lines)
// over the current screen with absolute positioning. Shared by the resume + rename modals.
func (m *aiMenu) appendModalBox(sb *strings.Builder, title string, lines []string) {
	innerW := 34
	for _, l := range lines {
		if w := visWidth(l); w+2 > innerW {
			innerW = w + 2
		}
	}
	if innerW > m.w-4 {
		innerW = m.w - 4
	}
	top := (m.h - (len(lines) + 2)) / 2
	if top < 1 {
		top = 1
	}
	left := (m.w - (innerW + 2)) / 2
	if left < 1 {
		left = 1
	}
	clr := "\x1b[38;5;215m" // modal accent (orange)
	rest := innerW - 1 - (visWidth(title) + 2)
	if rest < 0 {
		rest = 0
	}
	fmt.Fprintf(sb, "\x1b[%d;%dH%s╭─┤\x1b[1m%s\x1b[22m%s├%s╮%s",
		top, left, clr, title, clr, strings.Repeat("─", rest), reset)
	for i, l := range lines {
		fmt.Fprintf(sb, "\x1b[%d;%dH%s│%s %s %s│%s",
			top+1+i, left, clr, reset, padAnsi(l, innerW-2), clr, reset)
	}
	fmt.Fprintf(sb, "\x1b[%d;%dH%s╰%s╯%s",
		top+1+len(lines), left, clr, strings.Repeat("─", innerW), reset)
}

// asciiShrug fills an empty panel with a centered shrug + message.
func asciiShrug(w int, msg string) []string {
	c := func(s string) string {
		p := (w - visWidth(s)) / 2
		if p < 0 {
			p = 0
		}
		return strings.Repeat(" ", p) + s
	}
	return []string{
		"", "", "",
		c("\x1b[38;5;245m¯\\_(ツ)_/¯\x1b[0m"),
		"",
		c("\x1b[38;5;240m" + msg + "\x1b[0m"),
	}
}

func (m *aiMenu) footer() string {
	if m.apprActive() {
		return " \x1b[1;38;5;215m⚡ launch request — [y] approve · [n] decline · [esc] later\x1b[0m"
	}
	if m.picking {
		return " \x1b[38;5;240mresume picker — ↑↓ pick · ⏎ go · esc cancel\x1b[0m"
	}
	if m.renaming {
		return " \x1b[38;5;240mrenaming session — type a name in the box\x1b[0m"
	}
	if m.filter {
		return fmt.Sprintf(" \x1b[1;38;5;215m/\x1b[0m%s\x1b[7m \x1b[0m  \x1b[38;5;240m(enter: apply · esc: clear)\x1b[0m", m.query)
	}
	if m.flash != "" {
		clr := "\x1b[38;5;203m" // errors: red
		if strings.HasPrefix(m.flash, "✓") {
			clr = "\x1b[38;5;114m" // success: green
		}
		return " " + clr + m.flash + reset
	}
	if n := len(m.marked); n > 0 {
		return fmt.Sprintf(" \x1b[1;38;5;51m%d selected\x1b[0m \x1b[38;5;245m· ⏎ open all in one terminal · space toggle · esc clear\x1b[0m", n)
	}
	at := 0
	if len(m.rows) > 0 {
		at = m.cursor + 1
	}
	allTag := "show all"
	if m.showAll {
		allTag = "hide"
	}
	key := func(k, label string) string {
		return fmt.Sprintf("\x1b[48;5;236m\x1b[1;38;5;215m %s \x1b[0m\x1b[38;5;245m %s\x1b[0m", k, label)
	}
	if m.focusR { // detail pane focused: keys act on it
		return " " + key("↑↓", "scroll") + " " + key("←", "back") + " " +
			key("d", "diff") + " " + key("?", "help") + " " + key("q", "quit")
	}
	return " " + key("↑↓", "move") + " " + key("→←", "fold") + " " + key("P", "avail") + " " + key("O", "online") + " " + key("⏎", "open") + " " + key("esc", "session") + " " + key("S", "share") + " " + key("n", "term") + " " +
		key("space", "select") + " " + key("r", "rename") + " " + key("p", "pin") + " " + key("x", "archive") + " " + key("d", "diff") + " " + key("/", "search") + " " +
		key("s", "sort") + " " + key("R", "refresh") + " " + key("a", allTag) + " " + key("t", "theme") + " " + key("D", "docs") + " " + key("?", "help") + " " + key("q", "quit") +
		fmt.Sprintf("  \x1b[38;5;238m%d/%d\x1b[0m", at, len(m.rows))
}

// renderHelp draws the full-screen keybinding overlay (any key closes it).
func (m *aiMenu) renderHelp() {
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	sb.WriteString(padAnsi(" "+gradient("☎ P A R T Y L I N E")+"  \x1b[38;5;245m· keys\x1b[0m", m.w) + "\x1b[K\r\n")
	sb.WriteString("\x1b[K\r\n")
	rows := [][2]string{
		{"↑ ↓ · j k", "move the selection"},
		{"g · G", "jump to top · bottom"},
		{"→ ←", "expand · collapse a project (⏎ on a project header toggles it)"},
		{"P", "make a project joinable to parties — cycles off → Ask → Auto → off"},
		{"O", "go Online / Offline — advertise this machine's joinable projects to your party"},
		{"", "  when Online, launch requests pop a consent prompt: [y] approve · [n] decline · [esc] later"},
		{"⏎", "open the highlighted session (▸live = jump to the running one)"},
		{"esc", "jump back to the session you came from (▸live)"},
		{"space", "select; ⏎ then opens all selected into one terminal"},
		{"", "  once live: ctrl-\\ ←/→ or 1-9 switch · ctrl-\\ o manager · esc back · ctrl-\\ q quit"},
		{"S", "share this session — host it over the relay (view-only) for someone to join"},
		{"n", "open a plain shell as a new mux window (terminals + sessions in one tab)"},
		{"o", "open the session in a new terminal tab"},
		{"r", "rename (custom title for findability; empty resets)"},
		{"→ · tab", "focus the detail pane (↑↓ scroll · ← back)"},
		{"d", "view the session repo's git diff"},
		{"/", "search (esc clears)"},
		{"s", "cycle sort: last used · oldest · project"},
		{"R", "refresh the list (pick up newly-started sessions)"},
		{"a", "show / hide agent + automated sessions"},
		{"p · x", "pin · archive"},
		{"t", "cycle colour theme: Midnight (dark) · Daylight · Raging Unicorn · Cotton Sky · Paperwhite"},
		{"D", "open partyline docs in your browser"},
		{"?", "this help"},
		{"q", "quit (closes all live sessions)"},
	}
	for _, r := range rows {
		sb.WriteString(padAnsi(fmt.Sprintf("   \x1b[1;38;5;215m%-12s\x1b[0m \x1b[38;5;245m%s\x1b[0m", r[0], r[1]), m.w) + "\x1b[K\r\n")
	}
	for i := len(rows) + 2; i < m.h-1; i++ {
		sb.WriteString("\x1b[K\r\n")
	}
	sb.WriteString(padAnsi(" \x1b[38;5;240mpress any key to close\x1b[0m", m.w) + "\x1b[K")
	sb.WriteString("\x1b[J")
	os.Stdout.WriteString(themed(sb.String()))
}

// diffClosure returns a func that shows the session repo's `git diff` in a pager — or
// nil (setting a flash) if there's nothing to diff. The closure assumes the terminal is
// already in cooked mode on the main screen; the mux's suspend() handles that handoff.
func (m *aiMenu) diffClosure(s aiSession) func() {
	if s.Cwd == "" {
		m.flash = "no path recorded — can't diff"
		return nil
	}
	if _, err := os.Stat(s.Cwd); err != nil {
		m.flash = "directory is gone"
		return nil
	}
	return func() {
		pager := strings.Fields(os.Getenv("PAGER"))
		if len(pager) == 0 {
			pager = []string{"less", "-R"}
		}
		git := exec.Command("git", "-C", s.Cwd, "diff", "--color=always")
		pg := exec.Command(pager[0], pager[1:]...)
		pg.Stdin, _ = git.StdoutPipe()
		pg.Stdout, pg.Stderr = os.Stdout, os.Stderr
		git.Stderr = os.Stderr
		if err := pg.Start(); err == nil {
			_ = git.Run()
			_ = pg.Wait()
		}
	}
}

// presenceStatus renders the daemon presence line shown on the banner's second row:
// "daemon: online · advertising N · connected", or offline/connecting/error variants.
// Reflects the LAST state the presence goroutine recorded — the mux is input-driven, so an
// async transition (e.g. "connecting…" → "connected") shows on the next keystroke (S3 adds
// real-time delivery). Returns "" when presence is unavailable (piped, no mux).
func (m *aiMenu) presenceStatus() string {
	if m.presence == nil {
		return ""
	}
	dim := func(s string) string { return "\x1b[38;5;240m" + s + reset }
	mode, advertising, pending, note, _ := m.presence.snapshot()
	switch mode {
	case presConnecting:
		s := "\x1b[38;5;214m◌ daemon: connecting…" + reset
		if note != "" {
			s = "\x1b[38;5;214m◌ daemon: " + note + reset
		}
		return s
	case presOnline:
		s := "\x1b[38;5;46m● daemon: online" + reset + dim(" · advertising ") + "\x1b[38;5;252m" + itoa(advertising) + reset
		if pending > 0 {
			s += "\x1b[38;5;214m · " + itoa(pending) + " pending" + reset
		}
		if note != "" { // e.g. "reconnecting…"
			s += dim(" · " + note)
		}
		return s
	case presError:
		return "\x1b[38;5;203m✗ daemon: " + note + reset
	default: // offline
		return dim("○ daemon: offline · O to go online")
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func (m *aiMenu) hiddenCount() int {
	n := 0
	for _, s := range m.all {
		if isAgentSession(s) {
			n++
		}
	}
	return n
}

// ---- small render helpers -------------------------------------------------

// visWidth counts visible DISPLAY columns (emoji/CJK are 2 cells, not 1),
// skipping ANSI escape sequences. Rune-counting here was the bug that made
// rows "bounce": a ✅ in a preview rendered one column wider than counted,
// overflowed the line, and the terminal's auto-wrap shifted everything below.
func visWidth(s string) int {
	n := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			i++
			for i < len(rs) {
				if (rs[i] >= 'A' && rs[i] <= 'Z') || (rs[i] >= 'a' && rs[i] <= 'z') {
					break
				}
				i++
			}
			continue
		}
		n += runewidth.RuneWidth(rs[i])
	}
	return n
}

// padAnsi right-pads (or clips) ANSI-bearing text to exactly w display columns.
// Clipping can land one short of w (a 2-cell rune straddling the edge is
// dropped), so always re-measure and pad to the exact width.
func padAnsi(s string, w int) string {
	if visWidth(s) > w {
		s = clip(s, w)
	}
	return s + strings.Repeat(" ", max(0, w-visWidth(s)))
}

// clip truncates to w *visible* runes while copying ANSI escape sequences
// verbatim (so it never counts color codes toward width or severs one).
// Appends a reset to be safe.
func clip(s string, w int) string {
	var b strings.Builder
	vis := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b { // copy the whole escape sequence (ESC … final-letter)
			b.WriteRune(rs[i])
			i++
			for i < len(rs) {
				b.WriteRune(rs[i])
				if (rs[i] >= 'A' && rs[i] <= 'Z') || (rs[i] >= 'a' && rs[i] <= 'z') {
					break
				}
				i++
			}
			continue
		}
		rw := runewidth.RuneWidth(rs[i])
		if vis+rw > w { // a wide rune that would straddle the edge is dropped too
			b.WriteString(reset)
			return b.String()
		}
		b.WriteRune(rs[i])
		vis += rw
	}
	return b.String()
}

// wrap word-wraps s to width w, at most max lines (last line gets an ellipsis if
// truncated). Returns ["—"] for empty input so the pane never looks broken.
func wrap(s string, w, max int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{"\x1b[38;5;240m—\x1b[0m"}
	}
	if w < 8 {
		w = 8
	}
	var lines []string
	words := strings.Fields(s)
	cur := ""
	for _, word := range words {
		if runewidth.StringWidth(word) > w { // hard-break a very long token
			word = runewidth.Truncate(word, w-1, "") + "…"
		}
		if cur == "" {
			cur = word
		} else if runewidth.StringWidth(cur)+1+runewidth.StringWidth(word) <= w {
			cur += " " + word
		} else {
			lines = append(lines, cur)
			cur = word
			if len(lines) == max {
				break
			}
		}
	}
	if len(lines) < max && cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == max && len(lines) > 0 {
		last := lines[max-1]
		if runewidth.StringWidth(last) > w-1 {
			last = runewidth.Truncate(last, w-1, "")
		}
		lines[max-1] = last + "…"
	}
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
