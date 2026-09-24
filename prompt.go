package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"
)

// One set of line-oriented prompt helpers (audit item 12). Before this there were five spellings
// of the same yes/no question and ~15 prompts that never said how to get out. Two rules hold here:
//
//  1. EVERY prompt names its escape hatch, in the established `› ` idiom.
//  2. ok=false means CANCELLED — distinct from a deliberate "no" or an empty answer — so callers
//     can unwind a chain of prompts instead of marching on.
//
// Key decoding is menuKey()'s (menu_box.go): esc / ctrl-c / ctrl-\ cancel, arrows are swallowed.
// It returns 0 immediately when stdin isn't a tty, so nothing here can hang a pipe or the daemon.

var (
	stdinOnce sync.Once
	stdinBuf  *bufio.Reader
)

// stdin returns the ONE buffered reader every prompt shares. A second bufio.Reader over os.Stdin
// would silently swallow bytes the first had already buffered, so callers must not make their own.
func stdin() *bufio.Reader {
	stdinOnce.Do(func() { stdinBuf = bufio.NewReader(os.Stdin) })
	return stdinBuf
}

func stdinIsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// interactiveTTY reports whether there is a human on both ends — the precondition for asking a
// question at all. Used by surfaces that also run unattended (see closePRForBranch).
func interactiveTTY() bool { return stdinIsTTY() && stdoutIsTTY() }

// Confirm asks a yes/no question and names ALL THREE outcomes — [y] [n] [esc] — the one form that
// leaves no doubt there's a way out. The default is capitalised and is what enter picks. ok=false
// means cancelled (esc / ctrl-c / ctrl-\ / q / no tty); a deliberate no is (false, true).
func Confirm(prompt string, def bool) (val bool, ok bool) {
	y, n := "y", "n"
	if def {
		y = "Y"
	} else {
		n = "N"
	}
	fmt.Printf("  %s [%s]/[%s]/[%s] › ", prompt, y, n, "esc")
	for {
		switch menuKey() {
		case 0, 'q': // esc / ctrl-c / ctrl-\ / q / not a tty
			fmt.Printf("%s\n", dim("cancelled"))
			return false, false
		case '\n':
			fmt.Printf("%s\n", map[bool]string{true: "y", false: "n"}[def])
			return def, true
		case 'y':
			fmt.Println("y")
			return true, true
		case 'n':
			fmt.Println("n")
			return false, true
		}
	}
}

// Input reads one line, defaulting to def on empty. ok=false means cancelled: enter with no
// default, a lone q, or EOF/no tty. The hint copies wt_menu's model — the cancel is always stated.
func Input(prompt, def string) (string, bool) {
	hint := "enter or q cancels"
	if def != "" {
		hint = "enter for " + def + " · q cancels"
	}
	fmt.Printf("  %s %s › ", prompt, dim("("+hint+")"))
	if !stdinIsTTY() {
		fmt.Println()
		return "", false
	}
	line, err := stdin().ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		return "", false
	}
	s := strings.TrimSpace(line)
	if strings.EqualFold(s, "q") {
		return "", false
	}
	if s == "" {
		if def != "" {
			return def, true
		}
		return "", false
	}
	return s, true
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

// Pick prints a numbered list and reads a choice. ok=false means cancelled OR out of range — it
// never exits the process, which is what the old pickers did (fatal on a typo).
func Pick[T any](prompt string, items []T, show func(T) string) (idx int, ok bool) {
	if len(items) == 0 {
		return -1, false
	}
	for i, it := range items {
		fmt.Printf("    %s  %s\n", sgr(cgKey, fmt.Sprintf("%2d", i+1)), show(it))
	}
	s, ok := Input(prompt, "")
	if !ok {
		return -1, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(items) {
		fmt.Printf("  %s\n", dim(fmt.Sprintf("(not 1–%d — cancelled)", len(items))))
		return -1, false
	}
	return n - 1, true
}
