package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// One set of prompt helpers (audit item 12), now popup forms (popup_form.go). Two rules hold:
//
//  1. EVERY prompt names its escape hatch.
//  2. ok=false means CANCELLED — distinct from a deliberate "no" or an empty answer — so callers
//     can unwind a chain of prompts instead of marching on.
//
// With no terminal on stdin each one cancels at once, so nothing here can hang a pipe or the daemon.

func stdinIsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// interactiveTTY reports whether there is a human on both ends — the precondition for asking a
// question at all. Used by surfaces that also run unattended (see closePRForBranch).
func interactiveTTY() bool { return stdinIsTTY() && stdoutIsTTY() }

// Confirm asks a yes/no question in a popup form that names all three outcomes — y, n and a way
// out. The default is capitalised and is what enter picks. ok=false means cancelled (esc / ctrl-c /
// ctrl-\ / q / no tty); a deliberate no is (false, true).
func Confirm(prompt string, def bool) (val bool, ok bool) {
	if !formTerminal() {
		return false, false
	}
	return confirmForm(prompt, nil, prompt, def)
}

// Input reads one line in a popup form, defaulting to def on empty. ok=false means cancelled:
// enter with no default, a lone q, esc, or no tty.
func Input(prompt, def string) (string, bool) {
	if !formTerminal() {
		return "", false
	}
	return askForm(prompt, nil, prompt, def)
}

// confirmDestructive gates an irreversible action. `yes` short-circuits it (the --yes flag), and
// with no terminal to ask on it REFUSES rather than proceeding — a script that means it passes
// --yes, which is a one-word fix, whereas an unmeant deletion is not.
func confirmDestructive(what string, yes bool) bool {
	if yes {
		return true
	}
	if !interactiveTTY() {
		fmt.Printf("refusing to %s with no terminal to confirm on — pass --yes if you mean it\n", what)
		return false
	}
	val, ok := Confirm(what+"?", false)
	return ok && val
}

// takeYesFlag strips -y / --yes from args and reports whether it was there.
func takeYesFlag(args []string) ([]string, bool) {
	out, yes := make([]string, 0, len(args)), false
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			yes = true
			continue
		}
		out = append(out, a)
	}
	return out, yes
}

// Pick offers a numbered list in a popup form. ok=false means cancelled — it never exits the
// process, which is what the old pickers did (fatal on a typo).
func Pick[T any](prompt string, items []T, show func(T) string) (idx int, ok bool) {
	if len(items) == 0 || !formTerminal() {
		return -1, false
	}
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = show(it)
	}
	n, _, ok := (formModel{kind: formPick, title: prompt, label: prompt, items: labels}).run()
	return n, ok
}
