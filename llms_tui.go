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
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

// sessMeta is the per-session local curation state, persisted to a sidecar
// (~/.partyline/llms-meta.json) since we don't own the tools' session stores.
type sessMeta struct {
	Pinned      bool      `json:"pinned,omitempty"`
	Archived    bool      `json:"archived,omitempty"`
	Name        string    `json:"name,omitempty"`         // user-given title override (for findability)
	LastUsed    time.Time `json:"last_used,omitempty"`    // when YOU last opened it here (survives close)
	Repo        string    `json:"repo,omitempty"`         // git origin URL captured while the cwd was alive, so a session whose dir later vanishes can be recovered (recreate + clone)
	RepoChecked bool      `json:"repo_checked,omitempty"` // we've already looked for a remote at this session's cwd (even if none) — so we never re-fork git for it on later launches
	CwdOverride string    `json:"cwd_override,omitempty"` // relocation: when the dir MOVED (not deleted), the new path. Applied at load so gone-detection + resume use it transparently — the tool's own store still points at the old cwd.
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
		if v.Pinned || v.Archived || v.Name != "" || !v.LastUsed.IsZero() || v.Repo != "" || v.RepoChecked || v.CwdOverride != "" {
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
	firstRun  bool                // very first launcher open on this machine → welcome footer (E6.3)
	acctMu    sync.Mutex          // guards acct (a background Me() refresh may set it off the render goroutine)
	acct      string              // cached "logged in as" email, shown in the banner (empty = not logged in)
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
	// splitSetup: '|' asks the mux for the guided empty split. The manager can't open one
	// itself (only the mux owns panes), so this is a one-shot request flag, like spawnShell.
	splitSetup bool
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
	// narrow: this menu is an IN-PANE manager (a split pane, see llms_pane.go) — the detail
	// panel is dropped and modal boxes are collected into overlays instead of being emitted as
	// absolute-CUP escapes, because a pane's frame must stay position-independent. Always false
	// for the full-screen launcher, so its layout and bytes are unchanged.
	narrow   bool
	overlays []modalOverlay
	// confirmInstall: the "install always-on background service?" consent modal is up (cycling
	// presence online → always-on writes a launchd/systemd unit — a real system change).
	confirmInstall bool
}

// apprActive reports whether the launch-approval consent modal should own the screen + input:
// Online manager, ≥1 pending request, no other modal up, and not snoozed below a new arrival.
func (m *aiMenu) apprActive() bool {
	if m.presence == nil || m.picking || m.renaming || m.help || m.filter || m.confirmInstall {
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
	case "antigravity":
		return []permMode{
			{label: "default"},
			{label: "yolo", flags: []string{"--dangerously-skip-permissions"}, danger: true},
			{label: "custom…", custom: true},
		}
	}
	return nil
}

// toolLabel is the short, fixed-width-friendly name shown in the list (the Tool field
// itself stays the full product name for logic/dedup). "antigravity" is 11 chars and
// would break the 6-wide tool column, so it shows as its binary, "agy".
func toolLabel(tool string) string {
	if tool == "antigravity" {
		return "agy"
	}
	return tool
}

func toolColor(tool string) int {
	switch tool {
	case "claude":
		return 215 // orange
	case "codex":
		return 80 // cyan
	case "gemini":
		return 75 // blue
	case "antigravity":
		return 42 // green
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
	if bh <= 0 {
		// The menu is built and filtered BEFORE its size is known (m.h == 0 ⇒ bodyH() == -5), and
		// clamping against a negative body height pushes m.top to 6 — the launcher's first paint
		// would open scrolled 6 rows down (blank with a short list) until a keypress re-clamped.
		// Height-dependent clamping is deferred to the render paths, which run with a real m.h.
		return
	}
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
	if m.confirmInstall { // always-on install consent modal owns input
		return m.handleConfirmInstall(b)
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
	case 'O': // presence: cycle Offline → Online → Always-on → Offline
		m.cyclePresence()
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
	case '|': // guided split: two sessions side by side in ONE tab (same as ctrl-\ | in a session)
		m.splitSetup = true
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
			// Directory removed → RECOVER (framed modal: recreate/clone + resume)
			// instead of a doomed spawn into a missing cwd. Runs as a suspend.
			if s.Cwd != "" {
				if _, err := os.Stat(s.Cwd); err != nil {
					m.suspendFn = recoverModal(*s, m.meta[s.ID].Repo)
					return false, nil
				}
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

// cyclePresence advances the presence state on each [O]: Offline → Online → Always-on →
// Offline. Always-on is the installed OS service (a separate, persistent process) — entering
// it writes a launchd/systemd unit, so it goes through a consent modal; leaving it uninstalls.
func (m *aiMenu) cyclePresence() {
	if m.presence == nil {
		m.flash = "presence unavailable in this mode"
		return
	}
	switch {
	case serviceInstalled(): // Always-on → Offline (stop + remove the background service)
		if err := uninstallService(); err != nil {
			m.flash = "✗ " + err.Error()
		} else {
			m.flash = "✓ always-on off"
		}
	case m.presence.online(): // Online → Always-on (confirm — it's a system change)
		m.confirmInstall = true
	default: // Offline → Online (manager holds the stream while open)
		m.flash = m.presence.toggle()
	}
}

// handleConfirmInstall drives the always-on install consent modal. [y]/⏎ installs the OS
// service (handing the stream to it — the manager drops its own), [n]/esc cancels.
func (m *aiMenu) handleConfirmInstall(b []byte) (bool, *aiSession) {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		return false, nil // arrow/CSI — ignore
	}
	switch c := b[0]; {
	case c == 'y' || c == 'Y' || c == '\r' || c == '\n':
		m.confirmInstall = false
		m.presence.goOffline() // hand the stream to the service — never run two consumers
		if note, err := installService(); err != nil {
			m.flash = "✗ " + err.Error()
		} else {
			m.flash = "✓ always-on: " + note
		}
	case c == 'n' || c == 'N' || c == 0x1b:
		m.confirmInstall = false
		m.flash = "always-on cancelled"
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

func (m *aiMenu) render() { os.Stdout.WriteString(themed(m.frame())) } // re-skin to the theme

// frame builds the whole launcher screen as one string — exactly what render() writes. Its
// position-dependent escapes are confined to a leading CSI H, a per-line \x1b[K, a trailing
// \x1b[J and the modal boxes' absolute CUP, which is what lets paneLines() (llms_pane.go)
// relocate the same frame into a split pane.
func (m *aiMenu) frame() string {
	if m.help {
		return m.helpFrame()
	}
	listW := m.w * 52 / 100
	if listW < 32 {
		listW = 32
	}
	if listW > m.w-28 {
		listW = m.w - 28
	}
	detailW := m.w - listW - 1 // 1-col gap between panels
	// In a split pane there is no room for the detail panel: the list takes the whole width.
	// Only ever true for an in-pane manager, so the full-screen layout above is untouched.
	if m.narrow {
		listW, detailW = m.w, 0
	}
	bh := m.bodyH()

	left := m.listLines(listW-4, bh) // panel interior = width − "│ " − " │"
	var right []string
	if detailW > 0 {
		right = m.detailLines(detailW - 4)
	}
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
	title := brand.Wordmark() + "  \x1b[38;5;245m· session switchboard · " + counts + reset
	sb.WriteString(brand.PadTo(" "+title, m.w) + "\x1b[K\r\n")
	// Row 2: the tagline (left) + "logged in as …" + the daemon presence status
	// (right-aligned). Surfacing the account here is the cheap fix for cross-account
	// confusion — a daemon enabled on this machine binds to whoever is shown here.
	left2 := "   \x1b[3;38;5;240m" + m.tagline + reset
	status := m.presenceStatus()
	if acct := m.account(); acct != "" {
		who := "\x1b[38;5;245m" + acct + reset
		if status != "" {
			status = who + "  " + status
		} else {
			status = who
		}
	}
	gap := m.w - brand.VisWidth(left2) - brand.VisWidth(status) - 1
	if gap < 1 {
		gap = 1
	}
	sb.WriteString(brand.Clip(left2+strings.Repeat(" ", gap)+status, m.w) + "\x1b[K\r\n")

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
	sb.WriteString(boxTop(lTitle, listW, 111))
	if detailW > 0 {
		sb.WriteString(" " + boxTop(rTitle, detailW, rColor))
	}
	sb.WriteString("\x1b[K\r\n")

	// Body rows: interior lines wrapped in panel borders.
	for i := 0; i < bh; i++ {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		sb.WriteString(frameClr + "│" + reset + " " + brand.PadTo(l, listW-4) + " " + frameClr + "│" + reset)
		if detailW > 0 {
			r := ""
			if idx := i + m.dTop; idx < len(right) {
				r = right[idx]
			}
			sb.WriteString(" ")
			sb.WriteString(frameClr + "│" + reset + " " + brand.PadTo(r, detailW-4) + " " + frameClr + "│" + reset)
		}
		sb.WriteString("\x1b[K\r\n")
	}
	sb.WriteString(boxBottom(listW))
	if detailW > 0 {
		sb.WriteString(" " + boxBottom(detailW))
	}
	sb.WriteString("\x1b[K\r\n")
	sb.WriteString(brand.PadTo(m.footer(), m.w) + "\x1b[K")
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
	if m.confirmInstall { // overlay the always-on install consent modal
		m.appendInstallModal(&sb)
	}
	return sb.String()
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
			out = append(out, "\x1b[48;5;236m"+brand.PadTo(row, w)+reset)
		} else {
			out = append(out, brand.Clip(row, w))
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
		fg(toolColor(s.Tool)) + fmt.Sprintf("%-6s ", toolLabel(s.Tool)) + titleClr + title
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
	add := func(s string) { ls = append(ls, brand.Clip(s, w)) }
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
	add(fmt.Sprintf("\x1b[1;38;5;%dm%s\x1b[0m \x1b[38;5;245m%s\x1b[0m%s", toolColor(s.Tool), strings.ToUpper(toolLabel(s.Tool)), short(s.ID, 20), live))
	add("")
	loc := s.Cwd
	if loc == "" {
		loc = "(no path recorded)"
	}
	label("location", loc)
	ctx := ""
	if d.Gone {
		ctx = "\x1b[38;5;203m✗ gone — directory no longer exists\x1b[0m  \x1b[38;5;245m↵ recover\x1b[0m"
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
		"\x1b[1;38;5;231m" + brand.Clip(cur.label, 40) + "\x1b[0m \x1b[38;5;245m· " + cur.preset + "\x1b[0m",
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

// appendInstallModal overlays the always-on install consent prompt — entering Always-on writes
// an OS service (launchd/systemd) + runs the daemon in the background across reboots.
func (m *aiMenu) appendInstallModal(sb *strings.Builder) {
	lines := []string{
		"\x1b[1;38;5;231mGo always-on?\x1b[0m",
		"\x1b[38;5;245mInstalls a background service so this\x1b[0m",
		"\x1b[38;5;245mmachine stays available even with the\x1b[0m",
		"\x1b[38;5;245mmanager closed + across reboots.\x1b[0m",
		"",
		"\x1b[1;38;5;46m[y] install\x1b[0m   \x1b[38;5;240m[n] cancel\x1b[0m",
	}
	m.appendModalBox(sb, " ⚡ always-on ", lines)
}

// appendRenameModal overlays the session-rename editor as a centered modal.
func (m *aiMenu) appendRenameModal(sb *strings.Builder) {
	was := ""
	if s := m.selected(); s != nil {
		was = s.Title
	}
	lines := []string{
		"\x1b[38;5;245mwas:\x1b[0m " + brand.Clip(was, 46),
		"",
		"\x1b[1;38;5;231m" + m.renameBuf + "\x1b[7m \x1b[0m",
		"",
		"\x1b[38;5;240m⏎ save · empty resets · esc cancel\x1b[0m",
	}
	m.appendModalBox(sb, " rename session ", lines)
}

// modalOverlay is a modal box resolved to its screen position and rows, so it can be emitted
// as absolute-CUP escapes (full screen) OR composited into a line buffer (a split pane).
type modalOverlay struct {
	top, left int // 1-based screen position of the box's first row
	rows      []string
}

// appendModalBox draws a centered rounded modal box (embedded title, pre-styled lines)
// over the current screen with absolute positioning. Shared by the resume + rename modals.
// In narrow (in-pane) mode the box is COLLECTED instead of emitted — a pane's frame must stay
// position-independent, so paneLines() composites it into the rows itself.
func (m *aiMenu) appendModalBox(sb *strings.Builder, title string, lines []string) {
	ov := m.modalBox(title, lines)
	if m.narrow {
		m.overlays = append(m.overlays, ov)
		return
	}
	for i, row := range ov.rows {
		fmt.Fprintf(sb, "\x1b[%d;%dH%s", ov.top+i, ov.left, row)
	}
}

// modalBox resolves a modal's geometry + pre-styled rows. The row strings carry no positioning.
func (m *aiMenu) modalBox(title string, lines []string) modalOverlay {
	innerW := 34
	for _, l := range lines {
		if w := brand.VisWidth(l); w+2 > innerW {
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
	rest := innerW - 1 - (brand.VisWidth(title) + 2)
	if rest < 0 {
		rest = 0
	}
	rows := make([]string, 0, len(lines)+2)
	rows = append(rows, fmt.Sprintf("%s╭─┤\x1b[1m%s\x1b[22m%s├%s╮%s",
		clr, title, clr, strings.Repeat("─", rest), reset))
	for _, l := range lines {
		rows = append(rows, fmt.Sprintf("%s│%s %s %s│%s", clr, reset, brand.PadTo(l, innerW-2), clr, reset))
	}
	rows = append(rows, fmt.Sprintf("%s╰%s╯%s", clr, strings.Repeat("─", innerW), reset))
	return modalOverlay{top: top, left: left, rows: rows}
}

// asciiShrug fills an empty panel with a centered shrug + message.
func asciiShrug(w int, msg string) []string {
	c := func(s string) string {
		p := (w - brand.VisWidth(s)) / 2
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
	if m.confirmInstall {
		return " \x1b[1;38;5;215m⚡ go always-on? — [y] install background service · [n] cancel\x1b[0m"
	}
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
		return " " + brand.Pill("SEARCH") + fmt.Sprintf(" \x1b[1;38;5;215m/\x1b[0m%s\x1b[7m \x1b[0m  \x1b[38;5;240mtype to filter · ↵ apply · esc cancel\x1b[0m", m.query)
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
	if m.firstRun {
		// First launch on this machine (E6.3): instant value is the list above; the footer
		// points at the two next moves — the moat and finishing setup. One session only.
		return " \x1b[1;38;5;215m☎ welcome\x1b[0m \x1b[38;5;245m— ⏎ resumes a session right where it left off · " +
			"\x1b[0m\x1b[38;5;215m|\x1b[0m\x1b[38;5;245m opens two side by side · " +
			"give your agents a shared memory: \x1b[0m\x1b[38;5;215mctrl-\\ c\x1b[0m\x1b[38;5;245m in a session · " +
			"finish setup: partyline.sh/dashboard · ? all keys\x1b[0m"
	}
	// "| open in split" is in EVERY manager row, always second so it survives right-edge
	// truncation. It's a global action — it opens two pickers and doesn't read the current
	// selection — so gating it on "a session row happens to be highlighted" was wrong: the
	// switchboard opens with the tree COLLAPSED, so the very first thing you ever see is the
	// PROJECT row, and the one state that most needs to advertise the split didn't.
	if m.focusR { // detail pane focused: keys act on it
		return m.hintBar("DETAIL", []brand.Hint{
			{Key: "↑↓", Label: "scroll"}, {Key: "|", Label: "open in split"},
			{Key: "h", Label: "back"}, {Key: "q", Label: "quit"}})
	}
	if r := m.curRow(); r != nil && r.header() { // a project header is selected
		return m.hintBar("PROJECT", []brand.Hint{
			{Key: "→", Label: "expand"}, {Key: "|", Label: "open in split"},
			{Key: "P", Label: "joinable"}, {Key: "O", Label: "presence"},
			{Key: "/", Label: "search"}, {Key: "?", Label: "all keys"}})
	}
	// On a session row the two routes read as a pair: one session (↵) vs two (|).
	return m.hintBar("SESSION", []brand.Hint{
		{Key: "↵", Label: "open"}, {Key: "|", Label: "open in split"},
		{Key: "o", Label: "new tab"}, {Key: "S", Label: "share"},
		{Key: "d", Label: "diff"}, {Key: "/", Label: "search"},
		{Key: "?", Label: "all keys"}})
}

// hintBar is the contextual footer, bound to this frame's width. The rendering lives in
// internal/brand so the mux bar, the welcome screen, the in-session overlays and the ctrl-\
// menus all draw the identical thing.
func (m *aiMenu) hintBar(mode string, hints []brand.Hint) string {
	return brand.HintBar(mode, hints, m.w)
}

// helpFrame builds the keybinding overlay (any key closes it). Position-independent apart
// from the same leading CSI H / \x1b[K / \x1b[J as frame() — see paneLines.
func (m *aiMenu) helpFrame() string {
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	sb.WriteString(brand.PadTo(" "+brand.Wordmark()+"  \x1b[38;5;245m· keys\x1b[0m", m.w) + "\x1b[K\r\n")
	sb.WriteString("\x1b[K\r\n")
	rows := [][2]string{
		{"↑ ↓ · j k", "move the selection"},
		{"g · G", "jump to top · bottom"},
		{"→ ←", "expand · collapse a project (⏎ on a project header toggles it)"},
		{"P", "make a project joinable to parties — cycles off → Ask → Auto → off"},
		{"O", "presence: cycle Offline → Online → Always-on (background service) → Offline"},
		{"", "  when Online, launch requests pop a consent prompt: [y] approve · [n] decline · [esc] later"},
		{"⏎", "open the highlighted session (▸live = jump to the running one)"},
		{"esc", "jump back to the session you came from (▸live)"},
		{"space", "select; ⏎ then opens all selected into one terminal"},
		{"", "  once live: ctrl-\\ ←/→ or 1-9 switch · ctrl-\\ o manager · esc back · ctrl-\\ q quit"},
		{"S", "share this session — host it over the relay (view-only) for someone to join"},
		{"|", "open in split — pick two sessions, they become ONE tab (tab focus · z zoom · x close pane)"},
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
		sb.WriteString(brand.PadTo(fmt.Sprintf("   \x1b[1;38;5;215m%-12s\x1b[0m \x1b[38;5;245m%s\x1b[0m", r[0], r[1]), m.w) + "\x1b[K\r\n")
	}
	for i := len(rows) + 2; i < m.h-1; i++ {
		sb.WriteString("\x1b[K\r\n")
	}
	sb.WriteString(brand.PadTo(" \x1b[38;5;240mpress any key to close\x1b[0m", m.w) + "\x1b[K")
	sb.WriteString("\x1b[J")
	return sb.String()
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

// presenceStatus renders the visible 3-state presence selector on the banner's second row:
//
//	○ offline · ● online · ⚡ always-on        (the active state bright, the others dim)
//
// Always-on (the installed OS service) wins over the manager's own online/offline stream — it's
// a separate, persistent process. A short detail (advertising N · pending · connecting…) trails
// the active state. The mux is input-driven, so an async transition shows on the next repaint —
// but the daemon stream now calls Wake() on connect/pending, so it surfaces in real time.
// account / setAccount guard m.acct against the background identity refresh that
// may run on a different goroutine than render.
func (m *aiMenu) account() string {
	m.acctMu.Lock()
	defer m.acctMu.Unlock()
	return m.acct
}

func (m *aiMenu) setAccount(s string) {
	m.acctMu.Lock()
	m.acct = s
	m.acctMu.Unlock()
}

func (m *aiMenu) presenceStatus() string {
	if m.presence == nil {
		return ""
	}
	active, detail := "offline", ""
	if serviceInstalled() {
		active = "alwayson"
	} else {
		mode, advertising, pending, note, _ := m.presence.snapshot()
		switch mode {
		case presConnecting:
			active, detail = "online", " · connecting…"
		case presOnline:
			active, detail = "online", " · advertising "+itoa(advertising)
			if pending > 0 {
				detail += " · " + itoa(pending) + " pending"
			}
		case presError:
			detail = " · " + note // stays on "offline", surfaces the reason
		}
	}
	seg := func(key, glyph, label string, clr int) string {
		if active == key {
			return fmt.Sprintf("\x1b[1;38;5;%dm%s %s\x1b[0m", clr, glyph, label)
		}
		return fmt.Sprintf("\x1b[38;5;240m%s %s\x1b[0m", glyph, label)
	}
	bar := seg("offline", "○", "offline", 245) + dimSep() +
		seg("online", "●", "online", 46) + dimSep() +
		seg("alwayson", "⚡", "always-on", 226)
	if detail != "" {
		bar += "\x1b[38;5;245m" + detail + reset
	}
	return bar
}

func dimSep() string { return "\x1b[38;5;240m · " + reset }

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
