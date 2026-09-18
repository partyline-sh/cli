package main

// The ctrl-\ menu, as a real ptln modal. tmux's native display-menu couldn't be taught
// Left/Right navigation or the brand beyond border colors, and the field verdict was that it
// read as a second control interface. So the menu is now ours: a display-popup (centered —
// where people look) whose rounded amber border comes from the popup styling in the conf,
// running this TUI inside. All four arrows move, ⏎ opens, the printed hotkey fires directly,
// esc closes with nothing changed.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

type tmuxMenuItem struct {
	label  string
	key    string // printed hotkey ("" = none); matched case-sensitively (n vs N differ)
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
	merge   bool
	brk     bool
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
		f := strings.SplitN(line, "\t", 7)
		if len(f) != 7 {
			continue
		}
		if strings.TrimSpace(f[6]) == "1" {
			continue // the feed is not a session
		}
		kept = append(kept, paneRow{id: f[0], idx: f[1], name: f[2], active: f[3], spec: f[5]})
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
		"#{pane_id}\t#{window_index}\t#{window_name}\t#{window_active}#{pane_active}\t#{window_panes}\t#{@ptln_spec}\t#{@ptln_feed}").Output()
	kept, sessionPanes := parseSessionPanes(strings.Split(strings.TrimSpace(string(out)), "\n"))
	for _, r := range kept {
		id, idx, name, active := r.id, r.idx, r.name, r.active
		panes := strconv.Itoa(sessionPanes[idx])
		label, sessKey := name, ""
		if sp, ok := decodePaneSpec(r.spec); ok {
			// A shared window's name describes only whoever opened it first, so each merged
			// session is named by its own spec instead.
			if panes != "1" && sp.Label != "" {
				label = sp.Label
			}
			sessKey = sp.Key
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
		items = append(items, tmuxMenuItem{label: label, key: key, num: idx, paneID: id, sessKey: sessKey})
	}
	items = append(items,
		tmuxMenuItem{sep: true},
		tmuxMenuItem{label: "new session", key: "n", run: []string{"run-shell", self + " tmux --new"}},
		tmuxMenuItem{label: "new session — permissions bypassed", key: "N", run: []string{"run-shell", self + " tmux --new --bypass"}},
		tmuxMenuItem{label: "shell window", key: "t", run: []string{"new-window", "-b", "-t"}, target: 'l'},
		tmuxMenuItem{label: "launcher (full browser)", key: "o", run: []string{"run-shell", self + " tmux --home"}},
		// The work board as its own window — the operator surface that used to live only in a browser
		// tab. Reused rather than recreated when it is already open: the board is a view, and two of
		// them would just be two things to close.
		tmuxMenuItem{label: "work board — backlog · building · review", key: "b", run: boardWindowCmd(self)},
		// The feed is a READER — closing it loses nothing — so it toggles without confirmation
		// and opens without taking focus.
		tmuxMenuItem{label: "activity feed — show · hide", key: "f", run: []string{"run-shell", self + " tmux --feed"}},
		tmuxMenuItem{sep: true},
		tmuxMenuItem{label: "context thread — record · view · attach", key: "c", run: sessionMenuPopup(self, "c", "Context Threads")},
		tmuxMenuItem{label: "mcp servers for the highlighted session", key: "m", run: sessionMenuPopup(self, "m", "MCP Servers")},
		tmuxMenuItem{label: "fork to a git worktree", key: "w", run: sessionMenuPopup(self, "w", "Worktree")},
		tmuxMenuItem{label: "keep-going — auto-continue the agent", key: "g", run: sessionMenuPopup(self, "g", "Keep-going")},
		tmuxMenuItem{label: "peer messages — ask · answer · inject", key: "p", run: sessionMenuPopup(self, "p", "Peer Messages")},
		tmuxMenuItem{label: "share the highlighted session (view-only)", key: "S", run: sessionMenuPopup(self, "s", "Share")},
		tmuxMenuItem{sep: true},
		// -e: leave copy-mode automatically on reaching the bottom, the same way the wheel does.
		// Without it a pane entered from here stayed in copy-mode until someone pressed Esc —
		// and a pane in copy-mode SWALLOWS INPUT, so pasting into the agent silently did nothing
		// while copying out kept working. Nothing on screen said why.
		tmuxMenuItem{label: "scroll the highlighted session", key: "[", run: []string{"copy-mode", "-e", "-t"}, target: 'p'},
		tmuxMenuItem{label: "shell pane beside this one", key: "|", run: []string{"split-window", "-h", "-t"}, target: 'p'},
		// Arrow onto another session, press +, and it moves in beside the one you came from —
		// two live agents in one window, which is the thing the shell split above never gave you.
		tmuxMenuItem{label: "merge the highlighted session in here", key: "+", merge: true},
		tmuxMenuItem{label: "move it back to its own window", key: "-", brk: true},
		tmuxMenuItem{label: "close the highlighted session", key: "x", confirm: true, run: []string{"kill-pane", "-t"}, target: 'p'},
		tmuxMenuItem{label: "detach — everything keeps running", key: "q", run: []string{"detach-client", "-s", tmuxSessionName}},
		// The update-friendly exit: q leaves every engine alive (so nothing can self-update);
		// this one saves the workspace and stops them ALL — sessions and run windows alike —
		// and `ptln tmux --resume` reopens each conversation afterwards. Confirmed because it
		// interrupts anything mid-flight.
		tmuxMenuItem{label: "quit all — close every session (resume: ptln --resume)", key: "Q", confirm: true,
			run: []string{"run-shell", self + " tmux --quit"}},
	)
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
	// +5: rows, the hint line, the status-wall summary, and the border.
	want := len(items) + 5
	if clientRows > 4 && want > clientRows-2 {
		return clientRows - 2
	}
	return want
}

// tmuxClientRows is the attached client's height, 0 when it cannot be read.
func tmuxClientRows() int {
	out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{client_height}").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
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
	h := fmt.Sprintf("%d", tmuxMenuHeight(items, tmuxClientRows()))
	return tmuxCmd("display-popup", "-E", "-w", "72", "-h", h,
		"-T", "#[align=centre fg="+brand.Hex(brand.AmberRGB)+",bold] ☎ partyline ",
		selfExe(), "tmux", "--menu-tui").Run()
}

// tmuxMenuTUI runs inside the popup: render, navigate, act.
func tmuxMenuTUI() error {
	items := tmuxMenuItems()
	// The popup's own height, not the client's: tmux may have clamped it smaller than we asked.
	rows := 0
	if _, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		rows = h
	}
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	restore := func() { _ = term.Restore(fd, old); fmt.Print("\x1b[?25h") }
	defer restore()
	fmt.Print("\x1b[?25l")

	// origin = where the human was when the menu opened. Selection starts there; arrowing
	// onto another session row PREVIEWS it (the ribbon and the pane follow the menu — one
	// highlight, not two); esc returns to origin. That is the old bar selector's contract.
	origin := ""
	if out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}").Output(); err == nil {
		origin = strings.TrimSpace(string(out))
	}
	sel := 0
	for i, it := range items {
		if it.paneID != "" && it.paneID == origin {
			sel = i
			break
		}
	}
	for items[sel].sep {
		sel++
	}
	preview := func() {
		if id := items[sel].paneID; id != "" {
			_ = tmuxFocus(id)
		}
	}
	move := func(dir int) {
		for {
			sel += dir
			if sel < 0 {
				sel = len(items) - 1
			}
			if sel >= len(items) {
				sel = 0
			}
			if !items[sel].sep {
				preview()
				return
			}
		}
	}
	armed := -1 // index of a confirm item picked once; second pick acts, anything else disarms
	// The status column costs a full session-store scan (~300ms). The menu paints NOW and the
	// statuses land on the rows when the scan finishes — paintMu makes the late repaint and
	// the input loop's repaints take turns, and stale guards against painting over a menu the
	// user already left.
	var paintMu sync.Mutex
	stale := false
	paint := func() { fmt.Print(renderTmuxMenu(items, sel, armed, rows)) }
	go func() {
		statuses := tmuxSessionStatuses()
		paintMu.Lock()
		defer paintMu.Unlock()
		if stale {
			return
		}
		applyMenuStatuses(items, statuses)
		paint()
	}()
	defer func() { paintMu.Lock(); stale = true; paintMu.Unlock() }()
	launcherID := ensureLauncherWindow()
	pick := func(i int) error {
		it := items[i]
		// Only PANE-TARGETED destructive items ("close the highlighted session") are refused on the
		// launcher — quit-all is confirmed too but acts on the whole server, launcher included.
		if it.confirm && it.target == 'p' {
			if out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_id}").Output(); err == nil && strings.TrimSpace(string(out)) == launcherID {
				armed = -1
				_ = tmuxCmd("display-message", "the launcher stays — it can't be closed").Run()
				return errTmuxMenuArmed
			}
		}
		if it.confirm && armed != i {
			armed = i
			return errTmuxMenuArmed
		}
		if it.paneID != "" && len(it.run) == 0 {
			restore()
			return tmuxFocus(it.paneID)
		}
		if it.merge || it.brk {
			restore()
			return tmuxRearrange(it, origin)
		}
		restore()
		run := it.run
		switch it.target { // act on what's highlighted NOW — the selection moved the window
		case 'l': // insert to the LEFT of the launcher fixture
			if id := ensureLauncherWindow(); id != "" {
				run = append(append([]string{}, run...), id)
			} else {
				run = []string{"new-window", "-t", tmuxSessionName}
			}
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
		if out, err := tmuxCmd(run...).CombinedOutput(); err != nil {
			// surface the failure on the status line instead of dying — a non-zero exit
			// here becomes run-shell's black error page over the whole screen
			_ = tmuxCmd("display-message", strings.TrimSpace(it.label+": "+firstLineOf(string(out)))).Run()
		}
		return nil
	}

	var buf [64]byte
	for {
		paintMu.Lock()
		paint()
		paintMu.Unlock()
		n, err := os.Stdin.Read(buf[:])
		if err != nil || n == 0 {
			return nil
		}
		b := buf[:n]
		// consume EVERY sequence in the read — key repeat batches arrows into one read,
		// and handling only the first made held-arrow navigation lose steps
		for len(b) > 0 {
			switch {
			case b[0] == 0x1b && len(b) >= 3 && b[1] == '[':
				switch b[2] {
				case 'A', 'D': // up / left
					move(-1)
				case 'B', 'C': // down / right
					move(+1)
				}
				b = b[3:]
				continue
			case b[0] == 0x1b && len(b) == 1: // lone esc — back to the window you came from
				if origin != "" {
					_ = tmuxFocus(origin)
				}
				return nil
			case b[0] == 0x1b: // unrecognized escape sequence — drop it whole
				b = nil
				continue
			case b[0] == '\r' || b[0] == '\n':
				if err := pick(sel); err != errTmuxMenuArmed {
					return err
				}
				b = b[1:]
				continue
			case b[0] == 0x03 || b[0] == 0x1c: // ctrl-c / ctrl-\ close like esc
				if origin != "" {
					_ = tmuxFocus(origin)
				}
				return nil
			}
			hit := false
			for i, it := range items {
				if it.key != "" && string(b[:1]) == it.key {
					sel = i
					if err := pick(i); err != errTmuxMenuArmed {
						return err
					}
					hit = true
					break
				}
			}
			if !hit {
				armed = -1 // any other key stands down a pending confirm
			}
			b = b[1:]
		}
	}
}

// renderTmuxMenu paints the popup's interior: one row per item, the selection in the pill,
// hotkeys in amber, a hint line at the bottom. The popup border (rounded, amber) is drawn by
// tmux via the conf's popup styling — one frame, not a box inside a box.
var errTmuxMenuArmed = fmt.Errorf("armed")

// menuStatusWidth is the right-hand status column on session rows: "● needs you" / "▸ working",
// fixed-width so the column lines up into a scannable wall rather than a ragged edge.
const menuStatusWidth = 12

// menuStatusCell renders a session's status for the column. Amber for "waiting" — that session's
// agent has STOPPED and the next move is yours, which is the one state worth a colour; working is
// dim because working needs nothing from you. Plain text when the row is selected: the pill's
// background owns that row's colours.
func menuStatusCell(status string, selected bool) string {
	var text string
	switch status {
	case "waiting":
		text = "● needs you"
	case "active":
		text = "▸ working"
	default:
		return strings.Repeat(" ", menuStatusWidth)
	}
	padded := brand.PadTo(text, menuStatusWidth)
	if selected {
		return padded
	}
	if status == "waiting" {
		return "\x1b[38;2;255;152;56m" + padded + cgOff
	}
	return cgDim + padded + cgOff
}

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

// menuLabelWidth is what a row's text gets inside the 72-column popup, after the gutter. Labels
// were printed unclipped, so a long one ran past the border and wrapped into the next row.
const menuLabelWidth = 64

func renderTmuxMenu(items []tmuxMenuItem, sel, armed, rows int) string {
	amber := "\x1b[38;2;255;152;56m"
	var f strings.Builder
	f.WriteString("\x1b[2J\x1b[H")

	// Window the list to the rows the popup actually has. Printing every item into a popup tmux
	// clamped to the screen scrolled the top away silently — the first session was simply missing
	// from the menu, which is how a window you own becomes unreachable.
	start, end := menuWindow(items, sel, rows)
	if start > 0 {
		f.WriteString("  " + cgDim + "↑ more" + cgOff + "\x1b[K\r\n")
	}
	for i := start; i < end; i++ {
		it := items[i]
		if it.sep {
			f.WriteString("\r\n")
			continue
		}
		// One number per row, ONE colour for all of them. Dimming the indexes too wide to be a
		// hotkey encoded something true — 10 cannot be a single keypress — but it read as a
		// rendering fault, and three treatments of the same number (amber, dim, and none at all
		// on the selected row) is worse than the information was worth.
		gutter := "    "
		if k := it.key; k != "" {
			gutter = amber + brand.PadTo(brand.ShiftKey(k), 2) + cgOff + "  "
		} else if it.num != "" {
			gutter = amber + brand.PadTo(it.num, 2) + cgOff + "  "
		}
		label := it.label
		if i == armed {
			label = it.label + "  — sure? press " + brand.ShiftKey(it.key) + " again"
		}
		// Session rows carry a right-aligned status column; command rows get the full width.
		// The status cell is appended AFTER pad/clip — it carries its own colour codes, and
		// width arithmetic over escape sequences is how columns stop lining up.
		textW := menuLabelWidth
		if it.status != "" {
			textW = menuLabelWidth - menuStatusWidth - 2
		}
		if i == sel {
			// The selected row keeps its number. The pill used to swallow the gutter, so the one
			// row you were looking at was the one row that would not tell you its index.
			num := brand.ShiftKey(it.key)
			if it.key == "" {
				num = it.num
			}
			sl := brand.PadTo(num, 2) + "  " + label
			body := brand.PadTo(brand.ClipEllipsis(sl, textW), textW)
			if it.status != "" {
				body += "  " + menuStatusCell(it.status, true)
			}
			f.WriteString(fmt.Sprintf(" %s%s %s %s\x1b[K\r\n", brand.PillBg(), "\x1b[38;2;16;16;16m\x1b[1m", body, cgOff))
			continue
		}
		body := brand.ClipEllipsis(label, textW)
		if it.status != "" {
			body = brand.PadTo(body, textW) + "  " + menuStatusCell(it.status, false)
		}
		f.WriteString(fmt.Sprintf("  %s%s%s\x1b[K\r\n", gutter, body, cgOff))
	}
	if end < len(items) {
		f.WriteString("  " + cgDim + "↓ more" + cgOff + "\x1b[K\r\n")
	}
	if s := menuSummary(items); s != "" {
		f.WriteString("\r\n " + cgDim + s + cgOff + "\x1b[K")
	}
	f.WriteString("\r\n " + cgDim + "↑↓←→ preview · ⏎ keep · hotkey acts · esc go back" + cgOff + "\x1b[K")
	return f.String()
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
		"-T", "#[align=centre fg=#ff9838,bold] ☎ " + title + " ",
		self, "tmux", "--session-menu", which}
}

// boardWindowCmd opens (or returns to) the board window. `new-window -S` selects an existing window
// with that name instead of opening a second one — the board is a view of one board, and a stack of
// duplicates is just a stack of windows to close.
func boardWindowCmd(self string) []string {
	return []string{"new-window", "-S", "-n", boardWindowName, self, "board"}
}

// boardWindowName is what the board's window is called, and the name -S matches on.
const boardWindowName = "board"

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
		tmuxSaveWorkspace()
		return nil
	}

	if originWin == "" || pane == origin {
		return say("highlight the session you want to bring over, then press +")
	}
	// Read the launcher's key rather than calling ensureLauncherWindow: this guard must not be
	// the thing that creates a launcher, and a window whose key says launcher is one either way.
	if isLauncherWindow(win) || isLauncherWindow(originWin) {
		return say("the launcher stays in its own window")
	}
	// -b puts the arriving session on the LEFT of the one you were in, matching the order the
	// menu showed them in — the highlighted row sat above the row you came from.
	if out, err := tmuxCmd("join-pane", "-b", "-h", "-s", pane, "-t", originWin).CombinedOutput(); err != nil {
		return say("merge: " + firstLineOf(string(out)))
	}
	_ = tmuxFocus(pane)
	tmuxSaveWorkspace()
	return nil
}
