package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// newRunMenu is the `ctrl-\ n` overlay (E9 — menu parity): start anything without dropping to
// a terminal. Each door opens a NEW mux tab (SetPendingOpen), all starting in a directory you pick:
//  1. a fresh AI session (engine + optional thread / worktree / keep-going) — via the shared
//     newSessionSpec builder, so it's identical to `ptln new`.
//  2. a blank terminal — just your $SHELL in the chosen directory.
//
// The autonomous-task, crank and daemon-doctor doors lived here until those commands were removed.
func newRunMenu(mx sessionMenuTarget) {
	dir, _ := os.Getwd()
	if _, d, _, _, ok := mx.ActiveLaunch(); ok && d != "" {
		dir = d // start relative to the focused session's dir when there is one
	}
	m := formState(runForm(newRunForm()))
	switch string(m.key) {
	case "1":
		newSessionFlow(mx, dir)
	case "2":
		newShellFlow(mx, dir)
	}
}

// promptDir asks which directory to start in, defaulting to def on empty input. Expands a
// leading ~, resolves to an absolute path, and validates it's an existing directory. Returns
// ok=false when cancelled or when the entry doesn't resolve to a directory, so the caller aborts.
func promptDir(def string) (string, bool) {
	dir, ok := Input("directory", def)
	if !ok {
		return "", false
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		noteForm("Directory", []string{"✗ not a directory: " + dir})
		return "", false
	}
	return dir, true
}

// newShellFlow opens a plain shell in a chosen directory as a new tab.
func newShellFlow(mx sessionMenuTarget, dir string) {
	dir, ok := promptDir(dir)
	if !ok {
		return
	}
	mx.SetPendingOpen(*shellSpecIn(dir))
	noteForm("New / Run", []string{"✓ opening a terminal in " + dir + "…"})
}

// newSessionFlow gathers engine + options and queues the session spec (shared with `ptln new`).
// Six chained prompts, so EVERY ONE of them unwinds the whole flow on cancel (esc / q): an
// accidental chord must not trap you into launching a session you didn't want.
func newSessionFlow(mx sessionMenuTarget, dir string) {
	engines := []string{"claude", "codex", "gemini", "antigravity"}
	n, ok := Pick("engine", engines, func(e string) string { return e })
	if !ok {
		return
	}
	tool := engines[n]

	// Permissions, from the same table the share picker uses. The safe default is first and
	// Enter keeps it; the danger mode says what it costs on the line where it is chosen.
	var permFlags []string
	if modes := permissionModes(tool); modes != nil {
		pn, ok := Pick("permissions", modes, func(pm permMode) string {
			if pm.danger {
				return pm.label + "  (runs with NO prompts at all)"
			}
			return pm.label
		})
		if !ok {
			return
		}
		switch {
		case modes[pn].custom:
			raw, ok := Input("flags", "")
			if !ok {
				return
			}
			permFlags = strings.Fields(raw)
		default:
			permFlags = modes[pn].flags
		}
	}

	// Which directory to start in (defaults to the focused session's dir / cwd).
	dir, ok = promptDir(dir)
	if !ok {
		return
	}

	// Optional: attach a thread (pick from your threads, reusing the context menu's picker).
	thread := ""

	// Optional: isolate in a worktree. Empty = skip, so "skip" is its own default here.
	worktree := ""
	if wt, wok := Input("in a git worktree?", "skip"); !wok {
		return
	} else if wt != "skip" {
		worktree = wt
	}

	// Optional: keep-going (claude only; the menu still offers it, builder no-ops elsewhere).
	keepGoing, goal := 0, ""
	if tool == "claude" {
		kg, kok := Input("keep-going? max continuations", "none")
		if !kok {
			return
		}
		if kg != "none" {
			keepGoing, _ = strconv.Atoi(kg)
			if keepGoing > 0 {
				if g, gok := Input("goal (one line)", "none"); !gok {
					return
				} else if g != "none" {
					goal = g
				}
			}
		}
	}

	spec, err := newSessionSpec(tool, dir, thread, worktree, goal, false, thread == "", keepGoing, permFlags...)
	if err != nil {
		noteForm("New / Run", []string{fmt.Sprintf("✗ %v", err)})
		return
	}
	mx.SetPendingOpen(spec)
	noteForm("New / Run", []string{"✓ opening " + spec.Label + "…"})
}

// keepGoingToggleMenu is `ctrl-\ g` (E9.2): arm/disarm keep-going (E4.0) on the FOCUSED
// session mid-flow — for when it keeps stopping and you want it to push on. Arming relaunches
// the session wired with the Stop hook; disarming relaunches without it. claude-only.
func keepGoingToggleMenu(mx sessionMenuTarget) {
	argv, dir, label, key, ok := mx.ActiveLaunch()
	if !ok || len(argv) == 0 {
		noteForm("Keep-going", []string{"open an AI session first."})
		return
	}
	bin := argv[0]
	if bin != "claude" {
		noteForm("Keep-going", []string{"keep-going needs claude's Stop hook — not available for " + bin + " yet."})
		return
	}
	s, sok := askForm("Keep-going", []string{"auto-continue the agent after each turn, up to a hard cap or until it prints the done token — never a runaway."},
		"continuations to arm (a number · 0 or 'off' to disarm)", "")
	if !sok {
		return
	}
	thread, _ := mx.ActiveThreadInfo()
	mcps := mx.ActiveMCPs()
	wired, eng := wireSessionArgv(bin, carryConversation(bin, []string{bin}), thread, mcps)
	threadLabel := ""
	disarm := s == "0" || strings.EqualFold(s, "off")
	n, goal := 0, ""
	if !disarm {
		var err error
		if n, err = strconv.Atoi(s); err != nil || n < 1 {
			return
		}
		g, gok := Input("goal (one line)", "none")
		if !gok {
			return
		}
		if g != "none" {
			goal = g
		}
	}
	// Typing a number used to relaunch the session on the spot, with nothing between the keystroke
	// and the restart. The restart is the disruptive part (the agent reconnects), so it gets a gate.
	ask := fmt.Sprintf("restart this session with keep-going armed (%d continuations)?", n)
	if disarm {
		ask = "restart this session with keep-going off?"
	}
	if yes, cok := Confirm(ask, true); !cok || !yes {
		return
	}
	if !disarm {
		if k, err := armKeepGoing(n, goal); err == nil {
			wired = append(wired, "--settings", keepGoingSettings(k))
		}
	}
	mx.SetPendingReattach(prepareLaunchSpec(Spec{Label: label, Key: key + "·kg", Argv: wired, Dir: dir, Thread: thread, ThreadLabel: threadLabel, Engine: eng, MCPs: mcps}))
}
