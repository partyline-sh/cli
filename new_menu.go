package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptymux"
)

// newRunMenu is the `ctrl-\ n` overlay (E9 — menu parity): start anything without dropping to
// a terminal. Each door opens a NEW mux tab (SetPendingOpen), all starting in a directory you pick:
//  1. a fresh AI session (engine + optional thread / worktree / keep-going) — via the shared
//     newSessionSpec builder, so it's identical to `ptln new`.
//  2. a blank terminal — just your $SHELL in the chosen directory.
//  3. an autonomous task — spawns `ptln work …` as a tab you watch.
//  4. crank a backlog — spawns `ptln crank --file …` as a tab you watch.
//
// Uses the cg_menu palette. (3) and (4) run our own binary as a child, so no nested mux.
func newRunMenu(mx *ptymux.Mux) {
	in := stdin()
	dir, _ := os.Getwd()
	if _, d, _, _, ok := mx.ActiveLaunch(); ok && d != "" {
		dir = d // start relative to the focused session's dir when there is one
	}
	cgFrame("New / Run")
	fmt.Printf("  %sstart something new — opens in a new tab%s\n\n", cgDim, cgOff)
	cgItem("1", "new AI session", "claude · codex · gemini · antigravity")
	cgItem("2", "blank terminal", "just a shell, in a directory you pick")
	cgItem("3", "autonomous task", "one task, sandboxed worktree (ptln work)")
	cgItem("4", "crank a backlog", "drain a task file one at a time (ptln crank)")
	cgHintPrint("NEW")
	switch string(menuKey()) { // single keypress; q / esc / enter cancel
	case "1":
		newSessionFlow(mx, in, dir)
	case "2":
		newShellFlow(mx, in, dir)
	case "3":
		newWorkFlow(mx, in, dir)
	case "4":
		newCrankFlow(mx, in, dir)
	}
}

// promptDir asks which directory to start in, defaulting to def on empty input. Expands a
// leading ~, resolves to an absolute path, and validates it's an existing directory. Returns
// ok=false when cancelled or when the entry doesn't resolve to a directory, so the caller aborts.
func promptDir(def string) (string, bool) {
	fmt.Println()
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
		fmt.Printf("  %s\n", sgr(cgBad, "✗ not a directory: "+dir))
		return "", false
	}
	return dir, true
}

// newShellFlow opens a plain shell in a chosen directory as a new tab.
func newShellFlow(mx *ptymux.Mux, in *bufio.Reader, dir string) {
	dir, ok := promptDir(dir)
	if !ok {
		pause(in)
		return
	}
	mx.SetPendingOpen(*shellSpecIn(dir))
	fmt.Printf("\n  %s✓%s opening a terminal in %s…\n", cgOK, cgOff, dir)
	pause(in)
}

// newSessionFlow gathers engine + options and queues the session spec (shared with `ptln new`).
// Six chained prompts, so EVERY ONE of them unwinds the whole flow on cancel (esc / q): an
// accidental chord must not trap you into launching a session you didn't want.
func newSessionFlow(mx *ptymux.Mux, in *bufio.Reader, dir string) {
	engines := []string{"claude", "codex", "gemini", "antigravity"}
	fmt.Println("\n  engine:")
	n, ok := Pick("number", engines, func(e string) string { return e })
	if !ok {
		return
	}
	tool := engines[n]

	// Which directory to start in (defaults to the focused session's dir / cwd).
	dir, ok = promptDir(dir)
	if !ok {
		pause(in)
		return
	}

	// Optional: attach a thread (pick from your threads, reusing the context menu's picker).
	thread := ""
	yes, ok := Confirm("attach a context thread?", false)
	if !ok {
		return
	}
	if yes {
		if api.LoadToken() != "" {
			thread = cgPick(api.New()) // "" if cancelled → just no thread
		} else {
			fmt.Printf("  %s\n", dim("(log in first: ptln login)"))
		}
	}

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

	spec, err := newSessionSpec(tool, dir, thread, worktree, goal, false, thread == "", keepGoing)
	if err != nil {
		fmt.Printf("\n  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return
	}
	mx.SetPendingOpen(spec)
	fmt.Printf("\n  %s✓%s opening %s…\n", cgOK, cgOff, spec.Label)
	pause(in)
}

// newWorkFlow spawns `ptln work "<task>"` as a tab — the autonomous single-task runner.
func newWorkFlow(mx *ptymux.Mux, in *bufio.Reader, dir string) {
	fmt.Println()
	task, ok := Input("task", "")
	if !ok {
		return
	}
	argv := []string{selfExe(), "work", task}
	if wt, wok := Input("in a git worktree?", "cwd"); !wok {
		return
	} else if wt != "cwd" {
		argv = append(argv, "--worktree", wt)
	}
	bash, ok := Confirm("allow Bash? "+dim("(default: read/edit only)"), false)
	if !ok {
		return
	}
	if bash {
		argv = append(argv, "--allow-bash")
	}
	mx.SetPendingOpen(ptymux.Spec{
		Label: "⚙ work",
		Key:   fmt.Sprintf("work-%d", time.Now().UnixNano()),
		Argv:  argv, Dir: dir,
	})
	fmt.Printf("\n  %s✓%s running the task in a new tab — it leaves a branch to review.\n", cgOK, cgOff)
	pause(in)
}

// keepGoingToggleMenu is `ctrl-\ g` (E9.2): arm/disarm keep-going (E4.0) on the FOCUSED
// session mid-flow — for when it keeps stopping and you want it to push on. Arming relaunches
// the session wired with the Stop hook (rebuilt from scratch via wireSessionArgv, preserving
// thread + MCPs); disarming relaunches without it. claude-only.
func keepGoingToggleMenu(mx *ptymux.Mux) {
	in := stdin()
	argv, dir, label, key, ok := mx.ActiveLaunch()
	cgFrame("Keep-going")
	if !ok || len(argv) == 0 {
		fmt.Printf("  %sopen an AI session first.%s\n", cgDim, cgOff)
		pause(in)
		return
	}
	bin := argv[0]
	if bin != "claude" {
		fmt.Printf("  %skeep-going needs claude's Stop hook — not available for %s yet.%s\n", cgDim, bin, cgOff)
		pause(in)
		return
	}
	fmt.Printf("  %sauto-continue the agent after each turn, up to a hard cap or until it prints the done token — never a runaway.%s\n\n", cgDim, cgOff)
	s, sok := Input("continuations to arm "+dim("(a number · 0 or 'off' to disarm)"), "")
	if !sok {
		return
	}
	thread, _ := mx.ActiveThreadInfo()
	mcps := mx.ActiveMCPs()
	wired, eng := wireSessionArgv(bin, carryConversation(bin, []string{bin}), thread, mcps)
	threadLabel := ""
	if thread != "" && api.LoadToken() != "" {
		if th, _, e := api.New().GetThread(thread); e == nil && th != nil {
			threadLabel = th.Title
		}
	}
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
			fmt.Printf("\n  %s✓%s keep-going armed (%d continuations) — restarting the session…\n", cgOK, cgOff, n)
		}
	} else {
		fmt.Printf("\n  %s✓%s keep-going off — restarting the session…\n", cgOK, cgOff)
	}
	mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·kg", Argv: wired, Dir: dir, Thread: thread, ThreadLabel: threadLabel, Engine: eng, MCPs: mcps})
	pause(in)
}

// newCrankFlow spawns `ptln crank --file <f>` as a tab — the backlog loop.
func newCrankFlow(mx *ptymux.Mux, in *bufio.Reader, dir string) {
	fmt.Println()
	file, ok := Input("backlog file "+dim("(one task per line)"), "")
	if !ok {
		return
	}
	argv := []string{selfExe(), "crank", "--file", file}
	if m, mok := Input("max items?", "all"); !mok {
		return
	} else if m != "all" {
		argv = append(argv, "--max", m)
	}
	mx.SetPendingOpen(ptymux.Spec{
		Label: "⚙ crank",
		Key:   fmt.Sprintf("crank-%d", time.Now().UnixNano()),
		Argv:  argv, Dir: dir,
	})
	fmt.Printf("\n  %s✓%s cranking the backlog in a new tab — each item becomes a branch to review.\n", cgOK, cgOff)
	pause(in)
}
