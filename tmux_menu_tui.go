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
	"strings"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

type tmuxMenuItem struct {
	label   string
	key     string // printed hotkey ("" = none); matched case-sensitively (n vs N differ)
	winID   string // session row: the window it names (selection previews it live)
	run     []string
	target  rune // 'w'/'p': append the ACTIVE window/pane at act time — selection moves it
	sep     bool
	confirm bool // destructive: first pick arms, second pick acts — confirmed in THIS modal
}

// tmuxMenuItems composes the menu: every window, then the commands. Command targets are
// resolved when the command RUNS, not when the menu opens — arrowing through session rows
// switches the live window (the ribbon follows the menu), so "close this window" must mean
// the one highlighted now, never a snapshot from open time.
func tmuxMenuItems() []tmuxMenuItem {
	self := selfExe()
	var items []tmuxMenuItem
	out, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}\t#{window_index}\t#{window_name}\t#{window_active}").Output()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 {
			continue
		}
		id, idx, name, active := f[0], f[1], f[2], f[3]
		label := idx + "·" + name
		if active == "1" {
			label += "  ◀"
		}
		key := ""
		if len(idx) == 1 {
			key = idx
		}
		items = append(items, tmuxMenuItem{label: label, key: key, winID: id, run: []string{"select-window", "-t", id}})
	}
	items = append(items,
		tmuxMenuItem{sep: true},
		tmuxMenuItem{label: "new session", key: "n", run: []string{"run-shell", self + " tmux --new"}},
		tmuxMenuItem{label: "new session — permissions bypassed", key: "N", run: []string{"run-shell", self + " tmux --new --bypass"}},
		tmuxMenuItem{label: "shell window", key: "t", run: []string{"new-window", "-b", "-t"}, target: 'l'},
		tmuxMenuItem{label: "launcher (full browser)", key: "o", run: []string{"run-shell", self + " tmux --home"}},
		tmuxMenuItem{sep: true},
		tmuxMenuItem{label: "scroll the highlighted session", key: "[", run: []string{"copy-mode", "-t"}, target: 'p'},
		tmuxMenuItem{label: "split side-by-side", key: "|", run: []string{"split-window", "-h", "-t"}, target: 'p'},
		tmuxMenuItem{label: "close the highlighted window", key: "x", confirm: true, run: []string{"kill-window", "-t"}, target: 'w'},
		tmuxMenuItem{label: "detach — everything keeps running", key: "q", run: []string{"detach-client", "-s", tmuxSessionName}},
	)
	return items
}

// tmuxMenuHeight is the popup height for these items: rows + hint line + border.
func tmuxMenuHeight(items []tmuxMenuItem) int { return len(items) + 4 }

// tmuxMenuOpen launches the popup (the --menu bind target). Centered — display-popup's
// default — sized to the item list.
func tmuxMenuOpen() error {
	items := tmuxMenuItems()
	h := fmt.Sprintf("%d", tmuxMenuHeight(items))
	return tmuxCmd("display-popup", "-E", "-w", "56", "-h", h,
		"-T", "#[align=centre fg="+brand.Hex(brand.AmberRGB)+",bold] ☎ partyline ",
		selfExe(), "tmux", "--menu-tui").Run()
}

// tmuxMenuTUI runs inside the popup: render, navigate, act.
func tmuxMenuTUI() error {
	items := tmuxMenuItems()
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
	if out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_id}").Output(); err == nil {
		origin = strings.TrimSpace(string(out))
	}
	sel := 0
	for i, it := range items {
		if it.winID != "" && it.winID == origin {
			sel = i
			break
		}
	}
	for items[sel].sep {
		sel++
	}
	preview := func() {
		if id := items[sel].winID; id != "" {
			_ = tmuxCmd("select-window", "-t", id).Run()
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
		fmt.Print(renderTmuxMenu(items, sel, armed))
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
					_ = tmuxCmd("select-window", "-t", origin).Run()
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
					_ = tmuxCmd("select-window", "-t", origin).Run()
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

func renderTmuxMenu(items []tmuxMenuItem, sel, armed int) string {
	amber := "\x1b[38;2;255;152;56m"
	var f strings.Builder
	f.WriteString("\x1b[2J\x1b[H")
	for i, it := range items {
		if it.sep {
			f.WriteString("\r\n")
			continue
		}
		key := "   "
		if it.key != "" {
			key = fmt.Sprintf("%s%s%s  ", amber, brand.PadTo(it.key, 1), cgOff)
		}
		label := it.label
		if i == armed {
			label = it.label + "  — sure? press " + it.key + " again"
		}
		if i == sel {
			f.WriteString(fmt.Sprintf(" %s%s %s %s\x1b[K\r\n", brand.PillBg(), "\x1b[38;2;16;16;16m\x1b[1m", brand.PadTo(label, 48), cgOff))
			continue
		}
		f.WriteString(fmt.Sprintf("  %s%s%s\x1b[K\r\n", key, label, cgOff))
	}
	f.WriteString("\r\n " + cgDim + "↑↓←→ preview · ⏎ keep · hotkey acts · esc go back" + cgOff + "\x1b[K")
	return f.String()
}
