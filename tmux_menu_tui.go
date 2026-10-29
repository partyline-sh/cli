package main

// The ctrl-\ menu, as a real ptln modal. tmux's native display-menu couldn't be taught
// Left/Right navigation or the brand beyond border colors, and the field verdict was that it
// read as a second control interface. So the menu is now ours: a display-popup (centered —
// where people look) whose rounded amber border comes from the popup styling in the conf,
// running this TUI inside. All four arrows move, ⏎ opens, the printed hotkey fires directly,
// esc closes with nothing changed.

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"strconv"
	"strings"
)

type tmuxMenuItem struct {
	engine string // pane spec engine, searchable independently of the label
	label  string
	key    string // printed hotkey ("" = none); matched case-sensitively (n vs N differ)
	alt    string // a second hotkey that fires the same row, not printed (muscle-memory alias)
	num    string // window index, shown in the gutter even when too wide to be a hotkey
	paneID string // session row: the pane it names (selection previews it live)
	// status is the session's live state, right-aligned on the row: "waiting" (its agent stopped
	// and the next move is YOURS) or "active" (working). "" for command rows and for panes whose
	// state is unknowable from here (a shell window, an untagged run). See tmuxSessionStatuses.
	status  string
	sessKey string // the llms session id behind a session row — statuses key on it
	run     []string
	target  rune // 'w'/'p': append the ACTIVE window/pane at act time — selection moves it
	// merge/break rearrange the split instead of appending a target: see pick().
	merge bool
	brk   bool
	// rename: after this command runs, re-derive the window's name from the sessions still in it.
	// Closing one half of a pair left the survivor wearing the DEAD session's name — a tab reading
	// "codex · acr-cloud-aggreg" over a pane running ACR POS, naming a session that no longer
	// existed and misnaming the one that did.
	rename  bool
	sep     bool
	confirm bool // destructive: first pick arms, second pick acts — confirmed in THIS modal
}

// tmuxMenuItems composes the menu: every session, then the commands. Command targets are
// resolved when the command RUNS, not when the menu opens — arrowing through session rows
// switches the live pane (the ribbon follows the menu), so "close this session" must mean
// the one highlighted now, never a snapshot from open time.
// Statuses are applied SEPARATELY (applyMenuStatuses): computing them means scanning every
// session store on the machine (~300ms), and paying that before first paint — twice, since
// the popup's height sizing built the items too — is what made the menu lag. The menu now
// paints instantly and the statuses arrive on the rows a beat later.

// paneRow is one row of the session list, after the feed has been filtered out.
type paneRow struct {
	id, idx, name, active, spec string
	cmd                         string // pane_current_command — names a spec-less pane for what it is
	feed                        bool
}

// parseSessionPanes splits the pane list into the rows that are SESSIONS, and a count of session
// panes per window.
//
// The activity feed is a pane, and this list is one row per pane — so without filtering it
// appeared as a second row for whatever window it was opened beside, sharing that window's name
// and number: two rows reading "3 FLEET MANAGER", one of which could not be previewed, closed or
// merged as a session. "Close the highlighted session" then pointed at the wrong thing half the
// time.
//
// The count excludes feed panes too, because it decides whether a window is shared by two agents
// and therefore whether rows are renamed by their own spec. One agent plus a feed is one agent.
func parseSessionPanes(rows []string) ([]paneRow, map[string]int) {
	var kept []paneRow
	perWindow := map[string]int{}
	for _, line := range rows {
		f := strings.SplitN(line, "\t", 8)
		if len(f) < 7 {
			continue
		}
		if strings.TrimSpace(f[6]) == "1" {
			continue // the feed is not a session
		}
		cmd := ""
		if len(f) > 7 {
			cmd = strings.TrimSpace(f[7])
		}
		kept = append(kept, paneRow{id: f[0], idx: f[1], name: f[2], active: f[3], spec: f[5], cmd: cmd})
		perWindow[f[1]]++
	}
	return kept, perWindow
}

func tmuxMenuItems() []tmuxMenuItem {
	self := selfExe()
	var items []tmuxMenuItem
	seen := map[string]bool{}
	// One row per PANE, not per window: merged sessions share a window, and a window-shaped list
	// would show one row for two agents — leaving the second with no way to be picked, previewed
	// or broken back out.
	out, _ := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F",
		"#{pane_id}\t#{window_index}\t#{window_name}\t#{window_active}#{pane_active}\t#{window_panes}\t#{@ptln_spec}\t#{@ptln_feed}\t#{pane_current_command}").Output()
	kept, sessionPanes := parseSessionPanes(strings.Split(strings.TrimSpace(string(out)), "\n"))
	for _, r := range kept {
		id, idx, name, active := r.id, r.idx, r.name, r.active
		panes := strconv.Itoa(sessionPanes[idx])
		label, sessKey, engine := name, "", ""
		if sp, ok := decodePaneSpec(r.spec); ok {
			// A shared window's name describes only whoever opened it first, so each merged
			// session is named by its own spec instead.
			if panes != "1" && sp.Label != "" {
				label = sp.Label
			}
			sessKey, engine = sp.Key, sp.Engine
		} else if panes != "1" {
			// A pane with NO session spec in a shared window is a plain shell — the thing the old
			// `|` binding opened. Without this it inherited the window's name, so a session and a
			// stray shell rendered as two identical rows under one index, and the only way to tell
			// which was which was to open them. Name it for what it is.
			label = name + "  · " + shellPaneLabel(r.cmd)
		}
		if active == "11" {
			label += "  ◀"
		}
		key := ""
		// The digit hotkey is the window index, so it can only belong to one row of a merged
		// pair — arrows and ⏎ reach the rest.
		if len(idx) == 1 && !seen[idx] {
			key, seen[idx] = idx, true
		}
		// The index shows in the key column for EVERY window, bound or not. It used to also be
		// glued to the front of the label, so single-digit rows read "2  2·ACR ODOO MCP" while
		// double-digit ones read "10·LANDSEARCH" — the same number twice, inconsistently.
		items = append(items, tmuxMenuItem{label: label, key: key, num: idx, paneID: id, sessKey: sessKey, engine: engine})
	}
	items = append(items, tmuxCommandItems(self)...)
	return items
}

// tmuxSessionStatuses maps session id → "waiting" | "active" for every LIVE session on this
// machine — the same tail-read `ptln state` feeds the tray from, run once per menu open (the
// popup is a fresh process each time, so once per open is once, full stop). This is what turns
// the menu from a list of names into a status wall: which agents stopped and are waiting on YOU
// versus which are still working, visible before you pick where to land.
//
// Panes without a resolvable session (a shell window, an untagged run window) simply have no
// entry, and their rows render without a status — honest silence rather than a guess.
func tmuxSessionStatuses() map[string]string {
	out := map[string]string{}
	for _, s := range collectSessions() {
		if s.Live && s.Status != "" {
			out[s.ID] = s.Status
		}
	}
	return out
}

// tmuxMenuHeight is the popup height for these items: rows + hint line + border, CLAMPED to what
// the client can actually show.
//
// Unclamped, a list taller than the terminal made tmux size the popup to the screen while the TUI
// went on printing every row — so the top scrolled away and the first session simply was not in
// the menu, with nothing to say it had been cut.
func tmuxMenuHeight(items []tmuxMenuItem, clientRows int) int {
	// Fit the taller tab, its tab bar, context/summary, blank, hint, and border.
	m := menuModel{items: items}
	want := max(len(m.tabItems(0))+6, len(m.tabItems(1))+6)
	if clientRows > 4 && want > clientRows-2 {
		return clientRows - 2
	}
	return want
}

// tmuxClientDims is the attached client's width and height, 0, 0 when they cannot be read.
func tmuxClientDims() (int, int) {
	out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{client_width} #{client_height}").Output()
	if err != nil {
		return 0, 0
	}
	var w, h int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &w, &h)
	return w, h
}

// applyMenuStatuses fills the status column from the session scan — called off the paint
// path, because the scan is the expensive part (see tmuxMenuItems).
func applyMenuStatuses(items []tmuxMenuItem, statuses map[string]string) {
	for i := range items {
		if items[i].sessKey != "" {
			items[i].status = statuses[items[i].sessKey]
		}
	}
}

// tmuxMenuOpen launches the popup (the --menu bind target). Centered — display-popup's
// default — sized to the item list.
func tmuxMenuOpen() error {
	items := tmuxMenuItems()
	cols, rows := tmuxClientDims()
	// tmux refuses a popup larger than its client and shows nothing, so a narrow client gets a
	// narrower menu rather than none.
	w := 72
	if cols > 0 {
		w = max(min(w, cols-2), 32)
	}
	h := fmt.Sprintf("%d", tmuxMenuHeight(items, rows))
	return tmuxCmd("display-popup", "-E", "-w", fmt.Sprint(w), "-h", h,
		"-T", "#[align=centre fg="+tmuxTheme().Focus+",bold] ☎ partyline ",
		selfExe(), "tmux", "--menu-tui").Run()
}

// tmuxMenuTUI runs inside the popup: the Bubble Tea menu (tmux_menu_model.go), then whatever the
// pick asked for, once the terminal is restored.
func tmuxMenuTUI() error {
	items := tmuxMenuItems()
	// origin = where the human was when the menu opened. Selection starts there; arrowing onto
	// another session row PREVIEWS it (the ribbon and the pane follow the menu); esc returns to
	// origin. That is the old bar selector's contract.
	origin := ""
	if out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}").Output(); err == nil {
		origin = strings.TrimSpace(string(out))
	}
	preview := newMenuPreviewer(func(id string) { _ = tmuxFocus(id) })
	act := menuActions{
		focus:  preview.focus,
		goBack: func(id string) { _ = tmuxFocus(id) },
		pick:   func(it tmuxMenuItem) error { return menuPick(it, origin) },
	}
	final, err := tea.NewProgram(newMenuModel(items, origin, act)).Run()
	preview.close() // the last preview lands before anything the pick does
	if err != nil {
		return err
	}
	if m, ok := final.(menuModel); ok && m.after != nil {
		return m.after()
	}
	return nil
}

// tmuxSay puts a short message on the status line.
func tmuxSay(msg string) { _ = tmuxCmd("display-message", msg).Run() }

// tmuxActiveWindow is the active window of partyline's session, or "".
func tmuxActiveWindow() string {
	out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_id}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// menuPick does what a picked row asks, after the menu has closed.
func menuPick(it tmuxMenuItem, origin string) error {
	if it.paneID != "" && len(it.run) == 0 {
		return tmuxFocus(it.paneID)
	}
	if it.merge || it.brk {
		return tmuxRearrange(it, origin)
	}
	run := it.run
	switch it.target { // act on what's highlighted NOW — the selection moved the window
	case 'l':
		// "<session>:" — tmux 3.8 reads a bare session name as its window 0 and refuses
		// ("index 0 in use"); the colon asks for the next free index on every version.
		run = []string{"new-window", "-t", tmuxSessionName + ":"}
	case 'w', 'p':
		format := "#{window_id}"
		if it.target == 'p' {
			format = "#{pane_id}"
		}
		out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, format).Output()
		if err != nil {
			return nil
		}
		run = append(append([]string{}, run...), strings.TrimSpace(string(out)))
	}
	// The window this acted on, read BEFORE the command: killing the active pane can move the
	// active window, so asking afterwards names the wrong one.
	win := ""
	if it.rename {
		if out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_id}").Output(); err == nil {
			win = strings.TrimSpace(string(out))
		}
	}
	if out, err := tmuxCmd(run...).CombinedOutput(); err != nil {
		// surface the failure on the status line instead of dying — a non-zero exit here becomes
		// run-shell's black error page over the whole screen
		_ = tmuxCmd("display-message", strings.TrimSpace(it.label+": "+firstLineOf(string(out)))).Run()
	}
	if win != "" {
		tmuxRenameForPanes(win)
	}
	return nil
}

// menuStatusWidth is the right-hand status column on session rows: "‼ needs you" / "/ working",
// fixed-width so the column lines up into a scannable wall rather than a ragged edge.
const menuStatusWidth = 12

// menuSummary is the one-line status wall header: how many agents are live here and how many
// stopped waiting on you. "" when nothing is live — a zero-count banner is noise.
func menuSummary(items []tmuxMenuItem) string {
	live, waiting := 0, 0
	for _, it := range items {
		if it.status == "" {
			continue
		}
		live++
		if it.status == "waiting" {
			waiting++
		}
	}
	if live == 0 {
		return ""
	}
	if waiting == 0 {
		return fmt.Sprintf("%d agents working · none waiting on you", live)
	}
	return fmt.Sprintf("%d agents live · %d waiting on you", live, waiting)
}

// menuWindow is the slice of items to draw, keeping the selection on screen. rows <= 0 means the
// height is unknown, in which case everything is drawn — the old behaviour.
func menuWindow(items []tmuxMenuItem, sel, rows int) (int, int) {
	body := rows - 3 // the hint line, its blank, and the popup border
	if rows <= 0 || body >= len(items) || body < 1 {
		return 0, len(items)
	}
	start := sel - body/2
	if start < 0 {
		start = 0
	}
	if start+body > len(items) {
		start = len(items) - body
	}
	return start, start + body
}

// sessionMenuPopup composes the display-popup invocation for one per-session menu: the same
// cg modal UIs the built-in mux shows, run inside a centered popup against the highlighted
// window (the ctrl-\ menu's live preview means highlighted == active when this fires).
func sessionMenuPopup(self, which, title string) []string {
	return []string{"display-popup", "-E", "-w", "76", "-h", "26",
		"-T", "#[align=centre fg=" + tmuxTheme().Focus + ",bold] ☎ " + title + " ",
		self, "tmux", "--session-menu", which}
}

// gatePopup is the approval popup's display-popup command.
func gatePopup(self string) []string {
	w, h := gatePopupSize(tmuxClientSize())
	return []string{"display-popup", "-E", "-w", strconv.Itoa(w), "-h", strconv.Itoa(h), "-T", " approvals ", self, "loop", "gate"}
}

// gatePopupSize is the popup's size on a client of cols x rows: 96x30, or the client less a
// border where it is smaller. tmux refuses a popup larger than its client ("height too large")
// and shows nothing, so a fixed 96x30 never opened in a terminal under 30 rows, and no floor
// may make it larger than the client either.
func gatePopupSize(cols, rows int) (int, int) {
	w, h := 96, 30
	if cols > 0 {
		w = min(w, cols-2)
	}
	if rows > 0 {
		h = min(h, rows-2)
	}
	return max(w, 1), max(h, 1)
}

// tmuxClientSize is the current client's size, or 0, 0 when tmux cannot say.
func tmuxClientSize() (int, int) {
	out, err := tmuxCmd("display-message", "-p", "#{client_width} #{client_height}").Output()
	if err != nil {
		return 0, 0
	}
	var w, h int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &w, &h)
	return w, h
}

// loopsPopupCommand uses the launcher's client-clamped dimensions.
func loopsPopupCommand(self string, cols, rows int, contractorArgs ...string) []string {
	w, h := launcherPopupSize(cols, rows)
	args := []string{"display-popup", "-E", "-w", strconv.Itoa(w), "-h", strconv.Itoa(h), "-T", "#[align=centre fg=" + tmuxTheme().Focus + ",bold] ☎ loops ", self, "loop", "tab"}
	if i := indexOfArg(contractorArgs, "--contractor"); i >= 0 {
		args = append(args, "--contractor")
		if i+1 < len(contractorArgs) && contractorID.MatchString(contractorArgs[i+1]) {
			args = append(args, contractorArgs[i+1])
		}
	}
	return args
}

// tmuxRearrange handles the two items that move a session between windows rather than running a
// command against a target.
//
// Merge takes the session highlighted NOW and joins its pane into the window the human was in
// when the menu opened — "bring that one over here", which is why the destination is the origin
// and not the active window. tmux disposes of the source window once its last pane leaves.
//
// Break is the inverse, and the reason a merge is not a one-way door.
//
// Both save the workspace immediately. The detach hook would eventually catch the new layout,
// but a merge the human made and then lost to a crash would read as the feature not working.
func tmuxRearrange(it tmuxMenuItem, origin string) error {
	active, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}\t#{window_id}").Output()
	if err != nil {
		return nil
	}
	pane, win, _ := strings.Cut(strings.TrimSpace(string(active)), "\t")
	originWin := origin
	if origin != "" {
		if out, err := tmuxCmd("display-message", "-p", "-t", origin, "#{window_id}").Output(); err == nil {
			originWin = strings.TrimSpace(string(out))
		}
	}
	say := func(msg string) error {
		_ = tmuxCmd("display-message", msg).Run()
		return nil
	}

	if it.brk {
		if tmuxPaneCount(pane) < 2 {
			return say("that session already has a window to itself")
		}
		// -s is the pane to break out; -t would name a DESTINATION window and break the current
		// pane into it instead, which silently moves the wrong session.
		if out, err := tmuxCmd("break-pane", "-s", pane).CombinedOutput(); err != nil {
			return say("move out: " + firstLineOf(string(out)))
		}
		// Both sides are renamed: the window left behind drops back to a single name, and the pane
		// that moved out takes its own. Renaming only one leaves a composite tab describing a pair
		// that no longer exists.
		tmuxRenameForPanes(win)
		if out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_id}").Output(); err == nil {
			tmuxRenameForPanes(strings.TrimSpace(string(out)))
		}
		tmuxSaveWorkspace()
		return nil
	}

	if originWin == "" || pane == origin {
		return say("arrow onto the session you want beside this one, then press |")
	}
	// -b puts the arriving session on the LEFT of the one you were in, matching the order the
	// menu showed them in — the highlighted row sat above the row you came from.
	if out, err := tmuxCmd("join-pane", "-b", "-h", "-s", pane, "-t", originWin).CombinedOutput(); err != nil {
		return say("merge: " + firstLineOf(string(out)))
	}
	// The tab now holds two sessions, so it is named for both. Without this the ribbon kept the
	// name of whoever opened the window and the arriving session was invisible on it.
	tmuxRenameForPanes(originWin)
	_ = tmuxFocus(pane)
	tmuxSaveWorkspace()
	return nil
}

// sessionPopupTitles is what each per-session popup is called. Here rather than at the call site
// so the menu item carries only the key, and the title cannot drift from the thing it opens.
var sessionPopupTitles = map[string]string{
	"n": "New / Run", "N": "New AI Session",
	"w": "Worktree", "g": "Keep-going",
}

// tmuxSessionPopup opens one of the per-session menus as its own popup.
//
// IT MUST NOT BE CALLED FROM INSIDE THE MENU. The menu is itself a popup, and a popup cannot
// reliably open a popup: `display-popup` needs a current client, and a plain tmux subprocess
// launched from inside one does not have it — the error is "no current client", and the menu
// swallows it into a status-line flash nobody reads. Every item that opens a popup therefore goes
// through `run-shell -b`, which lets the menu's popup close and hands the work back to the server
// with a client in hand. That is exactly how ctrl-\ opens the menu in the first place.
func tmuxSessionPopup(which string) error {
	title := sessionPopupTitles[which]
	if title == "" {
		title = "partyline"
	}
	return tmuxCmd(sessionMenuPopup(selfExe(), which, title)...).Run()
}

// tmuxCommandItems is the fixed half of the ctrl-\ menu: every command row, in order.
// Split out from the session rows above it so `ptln help` renders the SAME list rather than a
// hand-copied one — a hand-copied key table drifted from this menu more than once.
func tmuxCommandItems(self string) []tmuxMenuItem {
	return []tmuxMenuItem{
		tmuxMenuItem{sep: true, label: "start and open"},
		// `n` opens the New/Run menu, which ASKS for the engine and the permission mode — the
		// same flow `ptln new` uses. It used to call --new directly, which launched whatever
		// quickNewEngine() returned: claude, unless PARTYLINE_ENGINE happened to be exported
		// before tmux started. So the menu decided silently, and starting a codex session meant
		// leaving the menu, setting an env var and relaunching. The picker was already built and
		// simply was not reachable from here.
		tmuxMenuItem{label: "new session — engine · permissions · worktree", key: "n", run: []string{"run-shell", "-b", self + " tmux --session-popup n"}},
		// N stays a one-keystroke launch for the case the picker exists to avoid asking about:
		// the engine you already use, with permissions off, right now.
		tmuxMenuItem{label: "new AI session — engine · permissions", key: "N", run: []string{"run-shell", "-b", self + " tmux --session-popup N"}},
		tmuxMenuItem{label: "shell window", key: "t", run: []string{"new-window", "-t"}, target: 'l'},
		tmuxMenuItem{label: "launcher (full browser)", key: "o", run: []string{"run-shell", "-b", self + " tmux --home"}},
		// The keyboard way to a contractor, for when a status chip cannot be clicked.
		tmuxMenuItem{label: "contractors — Loops Contractors tab", key: "k", run: []string{"run-shell", "-b", self + " tmux --loops-tab --contractor"}},
		// Who builds and reviews new batches, failover, the quality standard and disputes.
		tmuxMenuItem{label: "settings — who builds and reviews your loops", key: ",", run: []string{"run-shell", "-b", self + " tmux --settings-popup"}},
		// The approval popup for loops waiting on the person (UI plan step 2). run-shell -b, like
		// every row that opens a popup: the menu is itself a popup and cannot open another.
		tmuxMenuItem{label: "approve — loops waiting for you", key: "a", run: []string{"run-shell", "-b", self + " tmux --gate-popup"}},
		// The chat room as a popup; ctrl-t inside pins it as a tab.
		tmuxMenuItem{label: "chat — the room your sessions share", key: "c", run: []string{"run-shell", "-b", self + " tmux --chat-popup"}},
		// Open after this menu popup closes.
		tmuxMenuItem{label: "loops — what needs you, then planned, in progress, in review, done", key: "l", run: []string{"run-shell", "-b", self + " tmux --loops-tab"}},
		tmuxMenuItem{sep: true, label: "the session's agent"},
		tmuxMenuItem{label: "fork to a git worktree", key: "w", run: []string{"run-shell", "-b", self + " tmux --session-popup w"}},
		tmuxMenuItem{label: "keep-going — auto-continue the agent", key: "g", run: []string{"run-shell", "-b", self + " tmux --session-popup g"}},
		tmuxMenuItem{sep: true, label: "panes"},
		// -e: leave copy-mode automatically on reaching the bottom, the same way the wheel does.
		// Without it a pane entered from here stayed in copy-mode until someone pressed Esc —
		// and a pane in copy-mode SWALLOWS INPUT, so pasting into the agent silently did nothing
		// while copying out kept working. Nothing on screen said why.
		tmuxMenuItem{label: "scroll the highlighted session", key: "[", run: []string{"copy-mode", "-e", "-t"}, target: 'p'},
		// `|` JOINS TWO LIVE SESSIONS. It used to be split-window, which opens a fresh shell
		// beside the current pane — so the obvious key for "put these two side by side" was the
		// one key that could not do it, and the only key that could was labelled "merge". Both
		// the reporter and the man page expected | to pair two sessions; it now does.
		//
		// Arrow onto the other session first: the pane you were in when the menu opened stays
		// the destination, and the highlighted one moves in beside it.
		tmuxMenuItem{label: "split — bring the highlighted session in beside this one", key: "|", alt: "+", merge: true},
		tmuxMenuItem{label: "unsplit — move it back to its own tab", key: "-", brk: true},
		tmuxMenuItem{label: "shell pane beside this one", key: "\\", run: []string{"split-window", "-h", "-t"}, target: 'p'},
		tmuxMenuItem{label: "close the highlighted session", key: "x", confirm: true, rename: true, run: []string{"kill-pane", "-t"}, target: 'p'},
		tmuxMenuItem{sep: true, label: "leave"},
		tmuxMenuItem{label: "detach — everything keeps running", key: "q", run: []string{"detach-client", "-s", tmuxSessionName}},
		// The update-friendly exit: q leaves every engine alive (so nothing can self-update);
		// this one saves the workspace and stops them ALL — sessions and run windows alike —
		// and `ptln tmux --resume` reopens each conversation afterwards. Confirmed because it
		// interrupts anything mid-flight.
		tmuxMenuItem{label: "quit all — close every session (resume: ptln --resume)", key: "Q", confirm: true,
			run: []string{"run-shell", self + " tmux --quit"}},
	}
}

// shellPaneLabel names a pane that carries no partyline session, using whatever it is running.
// "shell" alone would be wrong for a pane someone left a build or a pager in.
func shellPaneLabel(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "shell"
	}
	switch cmd {
	case "zsh", "bash", "sh", "fish":
		return "shell"
	}
	return cmd
}
