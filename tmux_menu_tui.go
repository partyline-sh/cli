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

	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

type tmuxMenuItem struct {
	label  string
	key    string // printed hotkey ("" = none); matched case-sensitively (n vs N differ)
	num    string // window index, shown in the gutter even when too wide to be a hotkey
	paneID string // session row: the pane it names (selection previews it live)
	run    []string
	target rune // 'w'/'p': append the ACTIVE window/pane at act time — selection moves it
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
func tmuxMenuItems() []tmuxMenuItem {
	self := selfExe()
	var items []tmuxMenuItem
	seen := map[string]bool{}
	// One row per PANE, not per window: merged sessions share a window, and a window-shaped list
	// would show one row for two agents — leaving the second with no way to be picked, previewed
	// or broken back out.
	out, _ := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F",
		"#{pane_id}\t#{window_index}\t#{window_name}\t#{window_active}#{pane_active}\t#{window_panes}\t#{@ptln_spec}").Output()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 6)
		if len(f) != 6 {
			continue
		}
		id, idx, name, active, panes := f[0], f[1], f[2], f[3], f[4]
		label := name
		// A shared window's name describes only whoever opened it first, so each merged session
		// is named by its own spec instead.
		if panes != "1" {
			if sp, ok := decodePaneSpec(f[5]); ok && sp.Label != "" {
				label = sp.Label
			}
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
		items = append(items, tmuxMenuItem{label: label, key: key, num: idx, paneID: id})
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
		tmuxMenuItem{sep: true},
		tmuxMenuItem{label: "context thread — record · view · attach", key: "c", run: sessionMenuPopup(self, "c", "Context Threads")},
		tmuxMenuItem{label: "mcp servers for the highlighted session", key: "m", run: sessionMenuPopup(self, "m", "MCP Servers")},
		tmuxMenuItem{label: "fork to a git worktree", key: "w", run: sessionMenuPopup(self, "w", "Worktree")},
		tmuxMenuItem{label: "keep-going — auto-continue the agent", key: "g", run: sessionMenuPopup(self, "g", "Keep-going")},
		tmuxMenuItem{label: "peer messages — ask · answer · inject", key: "p", run: sessionMenuPopup(self, "p", "Peer Messages")},
		tmuxMenuItem{label: "share the highlighted session (view-only)", key: "S", run: sessionMenuPopup(self, "s", "Share")},
		tmuxMenuItem{sep: true},
		tmuxMenuItem{label: "scroll the highlighted session", key: "[", run: []string{"copy-mode", "-t"}, target: 'p'},
		tmuxMenuItem{label: "shell pane beside this one", key: "|", run: []string{"split-window", "-h", "-t"}, target: 'p'},
		// Arrow onto another session, press +, and it moves in beside the one you came from —
		// two live agents in one window, which is the thing the shell split above never gave you.
		tmuxMenuItem{label: "merge the highlighted session in here", key: "+", merge: true},
		tmuxMenuItem{label: "move it back to its own window", key: "-", brk: true},
		tmuxMenuItem{label: "close the highlighted session", key: "x", confirm: true, run: []string{"kill-pane", "-t"}, target: 'p'},
		tmuxMenuItem{label: "detach — everything keeps running", key: "q", run: []string{"detach-client", "-s", tmuxSessionName}},
	)
	return items
}

// tmuxMenuHeight is the popup height for these items: rows + hint line + border, CLAMPED to what
// the client can actually show.
//
// Unclamped, a list taller than the terminal made tmux size the popup to the screen while the TUI
// went on printing every row — so the top scrolled away and the first session simply was not in
// the menu, with nothing to say it had been cut.
func tmuxMenuHeight(items []tmuxMenuItem, clientRows int) int {
	want := len(items) + 4
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

// tmuxMenuOpen launches the popup (the --menu bind target). Centered — display-popup's
// default — sized to the item list.
func tmuxMenuOpen() error {
	items := tmuxMenuItems()
	h := fmt.Sprintf("%d", tmuxMenuHeight(items, tmuxClientRows()))
	return tmuxCmd("display-popup", "-E", "-w", "56", "-h", h,
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
	launcherID := ensureLauncherWindow()
	pick := func(i int) error {
		it := items[i]
		if it.confirm {
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
		fmt.Print(renderTmuxMenu(items, sel, armed, rows))
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

// menuLabelWidth is what a row's text gets inside the 56-column popup, after the gutter. Labels
// were printed unclipped, so a long one ran past the border and wrapped into the next row.
const menuLabelWidth = 48

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
		// One number per row. The gutter carries the window index whether or not it is short
		// enough to be a hotkey; an unbound index is dim, so what you can press stays obvious.
		gutter := "    "
		switch {
		case it.key != "":
			gutter = amber + brand.PadTo(it.key, 2) + cgOff + "  "
		case it.num != "":
			gutter = cgDim + brand.PadTo(it.num, 2) + cgOff + "  "
		}
		label := it.label
		if i == armed {
			label = it.label + "  — sure? press " + it.key + " again"
		}
		if i == sel {
			f.WriteString(fmt.Sprintf(" %s%s %s %s\x1b[K\r\n", brand.PillBg(), "\x1b[38;2;16;16;16m\x1b[1m",
				brand.PadTo(brand.ClipEllipsis(label, menuLabelWidth), menuLabelWidth), cgOff))
			continue
		}
		f.WriteString(fmt.Sprintf("  %s%s%s\x1b[K\r\n", gutter, brand.ClipEllipsis(label, menuLabelWidth), cgOff))
	}
	if end < len(items) {
		f.WriteString("  " + cgDim + "↓ more" + cgOff + "\x1b[K\r\n")
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
