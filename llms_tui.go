// Session data and key handling shared by the launcher popup.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
	uitheme "partyline.sh/partyline/internal/theme"
)

// sessMeta is the per-session local curation state (a pin, a title override, where a moved
// directory went), persisted to a sidecar (~/.partyline/llms-meta.json) since the tools' session
// stores are theirs, not ours. It no longer carries an `archived` flag: hiding a session read as
// deleting it, so `x` deletes for real and nothing needs hiding.
type sessMeta struct {
	Pinned      bool      `json:"pinned,omitempty"`
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
		if v.Pinned || v.Name != "" || !v.LastUsed.IsZero() || v.Repo != "" || v.RepoChecked || v.CwdOverride != "" {
			out[k] = v
		}
	}
	if b, err := json.MarshalIndent(out, "", "  "); err == nil {
		_ = os.WriteFile(llmMetaPath(), b, 0o600)
	}
}

// aiBrowse opens the launcher, or lists sessions when piped.
func aiBrowse() {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		aiList(nil)
		return
	}
	if err := runLLMSApp(nil); err != nil {
		fatal(fmt.Errorf("ptln llms: %w", err))
	}
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
	proj    string // the project key (session projKey()); set on both headers and sessions
	sessIdx int    // index into m.view, or -1 for a header
	count   int    // header only: number of sessions under it
	group   bool   // this row belongs to the project's nested "worktrees" group (header or session)
}

func (r treeRow) header() bool { return r.sessIdx < 0 }

type aiMenu struct {
	now            func() time.Time                // nil uses the wall clock; fixtures supply a fixed clock
	updateLine     string                          // "ptln X is available", read once when the launcher opens
	openTab        func(aiSession) (string, error) // optional host override for saved terminal tabs
	recoverSession func(aiSession, string) func()  // optional host recovery handoff

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
	meta      map[string]sessMeta // pin/rename sidecar, keyed by session id
	// permission-mode picker (shown on resume; see permMode)
	picking    bool
	pickModes  []permMode
	pickIdx    int
	pickArmed  bool // a danger mode is selected and awaiting a confirm press
	pickCustom bool // typing custom flags
	// confirmDelete is the session the delete modal is up for, held by VALUE: the modal owns input
	// until it is answered, and resolving the row again on the way out would let a refresh behind
	// it move the selection and delete something else. nil = no modal.
	confirmDelete *aiSession
	delFacts      deleteFacts
	pickText      string
	w, h          int
	help          bool // ? overlay open
	focusR        bool // detail (right) pane focused for scrolling
	dTop          int  // detail scroll offset (when focusR)
	dLen          int  // last-rendered detail line count (for scroll clamping)
	// Launcher state: live marks running sessions; suspendFn signals a pending pager action.
	live      map[string]bool
	suspendFn func()
	// rename: inline edit of a session's title override (persisted to the sidecar)
	renaming  bool
	renameBuf string
	// multi-select: ids checked with space; ⏎ opens them all into the mux at once
	marked         map[string]bool
	spawnShell     bool
	quickNew       bool
	quickNewBypass bool
	// splitSetup: '|' asks the mux for the guided empty split. The manager can't open one
	// itself (only the mux owns panes), so this is a one-shot request flag, like spawnShell.
	splitSetup bool
}

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
	// "openclaw", not ".openclaw" — a collapsed generated-workspace group keys on the tool's
	// dot-directory, and the dot is filesystem punctuation, not part of the name.
	return strings.TrimPrefix(filepath.Base(cwd), ".")
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

// wtGroupKey is the collapse-state key for a project's nested worktrees group, so it
// collapses independently of its parent project.
func wtGroupKey(proj string) string { return proj + "\x00worktrees" }

// rowKey is the collapse-state key of a header row.
func rowKey(r treeRow) string {
	if r.group {
		return wtGroupKey(r.proj)
	}
	return r.proj
}

// rowMember reports whether a session belongs under a header row: a project header owns
// every session of the project (worktrees included — it's the only row you see when the
// project is collapsed), a worktree-group header owns just the worktree ones.
func rowMember(r treeRow, s *aiSession) bool {
	return s.projKey() == r.proj && (!r.group || s.WtName != "")
}

// expandedRow reports whether a header row's children are shown.
func (m *aiMenu) expandedRow(r treeRow) bool {
	hasLive := false
	for i := range m.view {
		if rowMember(r, &m.view[i]) && m.live[m.view[i].ID] {
			hasLive = true
			break
		}
	}
	return m.isExpanded(rowKey(r), hasLive)
}

// buildRows derives the flattened tree (m.rows) from m.view: group by project, project
// order = first appearance in view (so the active sort still drives ordering), sessions
// kept in view order under each header. Sessions living in a git worktree of the project
// go into a nested, separately-collapsible "worktrees" group instead of becoming their own
// top-level project. Clamps the cursor to the new row count.
func (m *aiMenu) buildRows() {
	m.rows = m.rows[:0]
	order := make([]string, 0, 8)
	seen := map[string]bool{}
	idxs, wtIdxs := map[string][]int{}, map[string][]int{}
	for i := range m.view {
		s := &m.view[i]
		k := s.projKey()
		if !seen[k] {
			seen[k], order = true, append(order, k)
		}
		if s.WtName != "" {
			wtIdxs[k] = append(wtIdxs[k], i)
		} else {
			idxs[k] = append(idxs[k], i)
		}
	}
	for _, k := range order {
		ix, wx := idxs[k], wtIdxs[k]
		// The parent count includes the worktree sessions: collapsed, the header is the ONLY
		// thing you see, so it has to account for everything inside it.
		ph := treeRow{proj: k, sessIdx: -1, count: len(ix) + len(wx)}
		m.rows = append(m.rows, ph)
		if !m.expandedRow(ph) {
			continue
		}
		for _, i := range ix {
			m.rows = append(m.rows, treeRow{proj: k, sessIdx: i})
		}
		if len(wx) == 0 {
			continue
		}
		gh := treeRow{proj: k, sessIdx: -1, count: len(wx), group: true}
		m.rows = append(m.rows, gh)
		if m.expandedRow(gh) { // default collapsed (isExpanded), so worktrees stay out of the way
			for _, i := range wx {
				m.rows = append(m.rows, treeRow{proj: k, sessIdx: i, group: true})
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

// selectHeader puts the cursor on the header row matching a row's project (and group level).
func (m *aiMenu) selectHeader(want treeRow) {
	for i := range m.rows {
		if m.rows[i].header() && m.rows[i].proj == want.proj && m.rows[i].group == want.group {
			m.cursor = i
			return
		}
	}
	if want.group { // the group header is hidden (parent collapsed) → land on the project header
		m.selectHeader(treeRow{proj: want.proj, sessIdx: -1})
	}
}

// setRowCollapsed sets a header row's collapse state, rebuilds, and keeps the cursor on it.
func (m *aiMenu) setRowCollapsed(r treeRow, collapsed bool) {
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	m.collapsed[rowKey(r)] = collapsed
	m.buildRows()
	m.selectHeader(r)
	m.clampScroll()
}

// toggleCollapse flips the collapse state of the header under the cursor.
func (m *aiMenu) toggleCollapse() {
	r := m.curRow()
	if r == nil || !r.header() {
		return
	}
	m.setRowCollapsed(*r, m.expandedRow(*r)) // currently expanded → collapse, and vice-versa
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
		return uitheme.LegacyAccent
	case "codex":
		return uitheme.LegacyCodex
	case "gemini":
		return uitheme.LegacyGemini
	case "antigravity":
		return uitheme.LegacyAntigravity
	case "llm":
		return uitheme.LegacyLLM
	default:
		return uitheme.LegacyMuted
	}
}

func (m *aiMenu) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.query))
	m.view = m.view[:0]
	for _, s := range m.all {
		// `a` (showAll) reveals agent-spawned AND dead-worktree sessions. A session whose worktree
		// directory is gone can't be resumed and can't be recovered (the parent repo owns that
		// branch, not us), so it's dead weight in the default list — `ptln llms prune` deletes
		// those in one go, to the same trash `x` uses.
		if !m.showAll && (isAgentSession(s) || s.DeadWt) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(s.Tool + " " + filepath.Base(s.Cwd) + " " + projLabel(s.projKey()) + " " + s.WtName + " " + s.Title + " " + m.meta[s.ID].Name + " " + s.ID + " " + s.root)
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
			ba, bb := filepath.Base(a.projKey()), filepath.Base(b.projKey())
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

// refresh re-scans the session stores so newly-started (or ended) sessions appear and
// live status updates. Keeps the highlighted session selected by id across the rebuild,
// and drops cached detail so the right pane + status reflect reality.
func (m *aiMenu) refresh() {
	cur := ""
	if s := m.selected(); s != nil {
		cur = s.ID
	}
	m.all = launcherMenuScan()
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
	proj, wt, found := "", false, false
	for i := range m.view {
		if m.view[i].ID == id {
			proj, wt, found = m.view[i].projKey(), m.view[i].WtName != "", true
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
	if wt {
		m.collapsed[wtGroupKey(proj)] = false // …and its worktrees group, which starts collapsed
	}
	m.buildRows()
	for i := range m.rows {
		if !m.rows[i].header() && m.view[m.rows[i].sessIdx].ID == id {
			m.cursor = i
			m.clampScroll()
			return
		}
	}
}

// bodyH is the list height after reserving space for the popup chrome.
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
	if m.confirmDelete != nil { // the delete confirmation owns input until answered
		return m.handleConfirmDelete(b)
	}
	if m.help { // any key closes the help overlay
		m.help = false
		return false, nil
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
		case 'C': // →: expand a project (or worktrees-group) header; on a session, focus the detail pane
			if r := m.curRow(); r != nil && r.header() {
				m.setRowCollapsed(*r, false)
			} else {
				m.focusR = true
			}
		case 'D': // ←: collapse a header; on a session, unfocus detail or jump to its header
			if r := m.curRow(); r != nil && r.header() {
				m.setRowCollapsed(*r, true)
			} else if m.focusR {
				m.focusR, m.dTop = false, 0
			} else if r != nil {
				m.selectHeader(treeRow{proj: r.proj, sessIdx: -1, group: r.group})
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
	case 'p': // pin the highlighted session (persisted; no-op on a header)
		if s := m.selected(); s != nil {
			id := s.ID
			mt := m.meta[id]
			mt.Pinned = !mt.Pinned
			m.flash = map[bool]string{true: "✓ ★ pinned", false: "✓ unpinned"}[mt.Pinned]
			if m.meta == nil {
				m.meta = map[string]sessMeta{}
			}
			m.meta[id] = mt
			saveLLMMeta(m.meta)
			m.applyFilter()
		}
	case 'x': // delete the highlighted session — opens the confirmation (see llms_delete.go)
		if s := m.selected(); s != nil {
			if _, ok := sessionFiles(*s); !ok {
				m.flash = toolLabel(s.Tool) + " sessions share one store — there is no single file to delete"
				return false, nil
			}
			// A RUNNING session is refused outright rather than confirmed. The engine holds its
			// store open and rewrites it every turn, so there is no answer to "are you sure" that
			// makes moving the file safe — the modal would only offer a way to corrupt it.
			if s.Live {
				m.flash = "✗ " + trunc(s.Title, 32) + " is running — close it first"
				return false, nil
			}
			sel := *s
			m.confirmDelete = &sel
			m.delFacts = launcherMenuFacts(sel, m.view)
		}
	case 'o': // open in a new terminal tab; the menu stays up
		if s := m.selected(); s != nil {
			if s.resumeArgv == nil {
				m.flash = s.Tool + " sessions are list-only (no recorded path to resume into)"
				return false, nil
			}
			open := openInNewTab
			if m.openTab != nil {
				open = m.openTab
			}
			if msg, err := open(*s); err != nil {
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
				m.suspendFn = fn
			}
		}
	case 't': // cycle the colour theme (persisted) — for dark vs light terminals
		name, variant := cycleTheme()
		m.flash = "✓ theme: " + name + ", " + variant + " · ptln theme --background sets dark or light"
	case 'n': // New/Run menu (#160): AI session with options · terminal · work · crank
		m.spawnShell = true
	case 'N': // instant fresh session — default engine, current directory, zero questions.
		// The menu route is six interactions for the common case ("just give me claude, here").
		// One key for that case; the menu keeps every option for the uncommon ones.
		m.quickNew = true
	case 0x0e: // ctrl-n: the same instant session, permissions bypassed.
		// Same zero questions, one deliberate difference: the engine's own skip-permissions
		// mode. A separate chord rather than a prompt, because a prompt would un-make the
		// point of the key — and ctrl-n is close enough to N to learn, far enough not to hit
		// by accident.
		m.quickNewBypass = true
	case '|': // guided split: two sessions side by side in ONE tab (same as ctrl-\ | in a session)
		m.splitSetup = true
	case 'D': // open the partyline docs in your browser
		launcherMenuBrowser("https://partyline.sh/docs")
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
					recover := recoverModal
					if m.recoverSession != nil {
						recover = m.recoverSession
					}
					m.suspendFn = recover(*s, m.meta[s.ID].Repo)
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

// handleConfirmDelete drives the delete confirmation. [y]/⏎ moves the session's files to the
// trash; anything else cancels, so a stray keypress on a modal nobody meant to open is a no-op
// rather than a deletion.
func (m *aiMenu) handleConfirmDelete(b []byte) (bool, *aiSession) {
	s := m.confirmDelete
	m.confirmDelete = nil
	switch c := b[0]; {
	case c == 'y' || c == 'Y' || c == '\r' || c == '\n':
		dir, err := launcherMenuDelete(*s, m.meta)
		if err != nil {
			m.flash = "✗ " + err.Error()
			return false, nil
		}
		m.refresh() // the file is gone, so re-scanning the stores is what drops the row
		m.flash = "✓ deleted · recoverable from " + dir
	default:
		m.flash = "cancelled — nothing deleted"
	}
	return false, nil
}

// handleConfirmInstall drives the always-on install consent modal. [y]/⏎ installs the OS
// service (handing the stream to it — the manager drops its own), [n]/esc cancels.

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
		m.picking, m.pickArmed = false, false
		return false, nil
	}
	switch b[0] {
	case 'q':
		m.picking, m.pickArmed = false, false
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

const reset = "\x1b[0m"

func (m *aiMenu) detailLines(w int) []string {
	th := tmuxTheme()
	if len(m.view) == 0 {
		return asciiShrug(w, "no sessions match")
	}
	sel := m.selected()
	if sel == nil { // a project header is highlighted — nothing to preview
		if r := m.curRow(); r != nil {
			label := projLabel(r.proj)
			if r.group {
				label += " worktrees"
			}
			return asciiShrug(w, label+" · ⏎ or → to expand")
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
		add(fmt.Sprintf(th.LegacyForeground(uitheme.LegacyLabel)+"%-9s\x1b[0m%s", k, v))
	}

	live := ""
	switch s.Status {
	case "waiting":
		live = "  " + "\x1b[1m" + th.LegacyForeground(uitheme.LegacyWaiting) + "⏳ waiting for you\x1b[0m"
	case "active":
		live = "  " + "\x1b[1m" + th.LegacyForeground(uitheme.LegacyRunning) + "● running\x1b[0m"
	}
	add(fmt.Sprintf("\x1b[1m%s%s\x1b[0m "+th.LegacyForeground(uitheme.LegacyMuted)+"%s\x1b[0m%s", th.LegacyForeground(toolColor(s.Tool)), strings.ToUpper(toolLabel(s.Tool)), short(s.ID, 20), live))
	add("")
	loc := s.Cwd
	if loc == "" {
		loc = "(no path recorded)"
	}
	label("location", loc)
	// Named in the detail pane, not just glyphed in the row: resuming this runs the engine against
	// THAT home's config — its logins, its MCP wiring — and someone about to press ⏎ deserves the
	// full path rather than a truncated basename.
	if s.root != "" {
		label("home", th.LegacyForeground(uitheme.LegacyHome)+"⌂ "+s.root+"\x1b[0m  "+th.LegacyForeground(uitheme.LegacyMuted)+"resumes with HOME set here\x1b[0m")
	}
	ctx := ""
	if d.Gone {
		ctx = th.LegacyForeground(uitheme.LegacyDanger) + "✗ gone — directory no longer exists\x1b[0m  " + th.LegacyForeground(uitheme.LegacyMuted) + "↵ recover\x1b[0m"
	} else if d.Branch != "" {
		ctx = th.LegacyForeground(uitheme.LegacyBranch) + "⎇ " + d.Branch + "\x1b[0m"
	} else if s.Cwd != "" {
		ctx = th.LegacyForeground(uitheme.LegacyBranch) + "✓ exists\x1b[0m"
	}
	// Uncommitted-changes flag — "is this session's work saved?"
	if d.Dirty > 0 {
		ctx += fmt.Sprintf("  "+th.LegacyForeground(uitheme.LegacyAccent)+"⚠ %d uncommitted\x1b[0m", d.Dirty)
	} else if d.Dirty == 0 && !d.Gone {
		ctx += "  " + th.LegacyForeground(uitheme.LegacyClean) + "✓ clean\x1b[0m"
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
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	label("active", humanAgeAt(s.LastActive, now)+" ago")
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
	add(th.LegacyForeground(uitheme.LegacyRule) + strings.Repeat("╌", max(0, w)) + reset)
	add("\x1b[1m" + th.LegacyForeground(uitheme.LegacyLabel) + "first prompt\x1b[0m")
	for _, line := range wrap(d.First, w-2, 4) {
		add("  " + th.LegacyForeground(uitheme.LegacyBody) + line + reset)
	}
	// Recent conversation tail: the last few messages, role-labelled. The pane scrolls
	// (→/tab to focus, ↑↓), so this can run long. Falls back to the single latest message
	// for stores we can't break into a tail.
	if len(d.Recent) > 0 {
		add("")
		add("\x1b[1m" + th.LegacyForeground(uitheme.LegacyLabel) + "recent\x1b[0m")
		for _, rm := range d.Recent {
			label, clr := rm.Role, uitheme.LegacyMuted
			switch rm.Role {
			case "user":
				label, clr = "you", uitheme.LegacyYou
			case "assistant", "model":
				label, clr = "agent", uitheme.LegacyAccent
			case "tool", "tool_use", "tool_result":
				label, clr = "tool", uitheme.LegacyClean
			}
			add(fmt.Sprintf("\x1b[1m%s%s\x1b[0m", th.LegacyForeground(clr), label))
			for _, line := range wrap(rm.Text, w-2, 6) {
				add("  " + th.LegacyForeground(uitheme.LegacyBody) + line + reset)
			}
		}
	} else if d.Last != "" && d.Last != d.First {
		add("")
		add("\x1b[1m" + th.LegacyForeground(uitheme.LegacyLabel) + "latest\x1b[0m")
		for _, line := range wrap(d.Last, w-2, 4) {
			add("  " + th.LegacyForeground(uitheme.LegacyBody) + line + reset)
		}
	}
	// Context: what this session runs *with* — its memory file, MCP servers, skills.
	if len(d.Memory) > 0 || len(d.MCP) > 0 || len(d.Skills) > 0 {
		add(th.LegacyForeground(uitheme.LegacyRule) + strings.Repeat("╌", max(0, w)) + reset)
		add("\x1b[1m" + th.LegacyForeground(uitheme.LegacyLabel) + "context\x1b[0m")
		for _, mf := range d.Memory {
			add(fmt.Sprintf("  "+th.LegacyForeground(uitheme.LegacyBody)+"%s\x1b[0m "+th.LegacyForeground(uitheme.LegacyLabel)+"· %d lines · %s\x1b[0m", filepath.Base(mf.Path), mf.Lines, mf.Scope))
		}
		if len(d.MCP) > 0 {
			add("  " + th.LegacyForeground(uitheme.LegacyLabel) + "mcp\x1b[0m")
			for _, ln := range wrap(strings.Join(d.MCP, ", "), w-2, 4) {
				add("    " + th.LegacyForeground(uitheme.LegacyBody) + ln + reset)
			}
		}
		if len(d.Skills) > 0 {
			add(fmt.Sprintf("  "+th.LegacyForeground(uitheme.LegacyLabel)+"skills\x1b[0m "+th.LegacyForeground(uitheme.LegacyLabel)+"(%d)\x1b[0m", len(d.Skills)))
			for _, ln := range wrap(strings.Join(d.Skills, ", "), w-2, 4) {
				add("    " + th.LegacyForeground(uitheme.LegacyBody) + ln + reset)
			}
		}
	}
	return ls
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
		c(tmuxTheme().LegacyForeground(uitheme.LegacyMuted) + "¯\\_(ツ)_/¯\x1b[0m"),
		"",
		c(tmuxTheme().LegacyForeground(uitheme.LegacyFaint) + msg + "\x1b[0m"),
	}
}

// diffClosure returns a func that shows the session repo's `git diff` in a pager — or
// nil (setting a flash) if there's nothing to diff.
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
		cmd := launcherDiffCommand(s)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = cmd.Run()
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// ---- small render helpers -------------------------------------------------

// wrap word-wraps s to width w, at most max lines (last line gets an ellipsis if
// truncated). Returns ["—"] for empty input so the pane never looks broken.
func wrap(s string, w, max int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{tmuxTheme().LegacyForeground(uitheme.LegacyFaint) + "—\x1b[0m"}
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

// aiMenuKeys is the session manager's key table: the `?` overlay and `ptln help` both render
// it, so the two cannot disagree. They did — help advertised keys the manager does not have and
// omitted n, N, |, r, d, t, P, O and D, which it does.
var aiMenuKeys = [][2]string{
	{"↑ ↓ · j k", "move the selection"},
	{"g · G", "jump to top · bottom"},
	{"→ ←", "expand · collapse a project (⏎ on a project header toggles it)"},
	{"⏎", "open the highlighted session (▸live = jump to the running one)"},
	{"esc", "jump back to the session you came from (▸live)"},
	{"space", "select; ⏎ then opens all selected into one terminal"},
	{"", "  once live: ctrl-\\ ←/→ or 1-9 switch · ctrl-\\ o manager · esc back · ctrl-\\ q quit"},
	{"|", "open in split — pick two sessions, they become ONE tab (tab focus · z zoom · x close pane)"},
	{"N", "new session NOW — default engine, this directory, no questions"},
	{"ctrl-n", "the same, with permissions bypassed"},
	{"n", "new — an AI session with options, or a plain terminal"},
	{"o", "open the session in a new terminal tab"},
	{"r", "rename (custom title for findability; empty resets)"},
	{"→ · tab", "focus the detail pane (↑↓ scroll · ← back)"},
	{"d", "view the session repo's git diff"},
	{"/", "search (esc clears)"},
	{"s", "cycle sort: last used · oldest · project"},
	{"R", "refresh the list (pick up newly-started sessions)"},
	{"a", "show / hide agent + automated sessions"},
	{"p", "pin"},
	{"x", "delete — confirms first, showing branch, uncommitted work, last use and live siblings"},
	{"", "  files move to ~/.partyline/trash (recoverable); a RUNNING session is refused"},
	{"t", "cycle colour theme: Partyline · Blue · Mono · Paperwhite (ptln theme sets the background)"},
	{"D", "open partyline docs in your browser"},
	{"?", "this help"},
	{"q", "quit (closes all live sessions)"},
}
