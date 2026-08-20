package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// llms_roots_cmd.go — `ptln llms --look-in` and `--roots`: tell the session manager where else to
// look, and see where it is already looking.
//
// WHY THIS EXISTS. The capability was already built — collectSessions() walks every root in
// session-roots.json, and resuming from an adopted root points HOME at it — but the ONLY way to add
// one was a modal that appears when you have ZERO sessions. Anyone with a session of their own could
// never reach it, and a session under a different $HOME stayed invisible with nothing on screen to
// suggest why. That is the same shape as the other bugs this week: a real capability with no door.
//
// The concrete case: a session started as `HOME=/home/acr claude --resume <id>` lives under
// /home/acr/.claude/. A session manager running as another user cannot see it, reports nothing
// unusual, and offers no way to say where to look.

// llmsLookIn adopts a directory as a session root.
func llmsLookIn(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: ptln llms --look-in <dir>\n" +
			"  <dir> is the HOME a session was started under — the one whose .claude/.codex/.gemini\n" +
			"  directories hold the stores. `ptln llms --roots` shows where it looks today."))
	}
	raw := args[0]
	abs, err := filepath.Abs(expandTilde(raw))
	if err != nil {
		fatal(fmt.Errorf("%s: %w", raw, err))
	}
	if fi, serr := os.Stat(abs); serr != nil || !fi.IsDir() {
		fatal(fmt.Errorf("%s is not a directory. Pass the HOME a session ran under — for a session\n"+
			"started with `HOME=/home/acr claude ...` that is /home/acr, not the project directory.", abs))
	}
	if self, _ := os.UserHomeDir(); abs == self {
		fmt.Printf("%s is already this account's home — it is always searched.\n", abs)
		return
	}

	// Say plainly when there is no store there, but ADOPT IT ANYWAY. A home whose first session has
	// not been created yet is a legitimate thing to point at, and refusing would send someone back to
	// guessing. Reporting it is what stops a typo looking like a working setup.
	if !hasSessionStore(abs) {
		fmt.Printf("⚠ no AI session store found under %s (looked for .claude, .codex, .gemini, .antigravity)\n", abs)
		fmt.Println("  Adding it anyway — sessions created there later will show up.")
	}
	if err := addSessionRoot(abs); err != nil {
		fatal(fmt.Errorf("could not record that root: %w", err))
	}

	// Prove it worked by COUNTING, not by claiming. "Added" is what a broken path prints too.
	n := 0
	for _, s := range collectSessions() {
		if s.root == abs {
			n++
		}
	}
	fmt.Printf("✓ also looking in %s\n", abs)
	switch n {
	case 0:
		fmt.Println("  No sessions there yet. If you expected some, check the path is the HOME the")
		fmt.Println("  session ran under, then run `ptln llms --roots`.")
	case 1:
		fmt.Println("  1 session found there. Run `ptln` to open it — it resumes with HOME set to that root.")
	default:
		fmt.Printf("  %d sessions found there. Run `ptln` to open them — they resume with HOME set to that root.\n", n)
	}
}

// llmsRoots prints every place the session manager looks, and what each one contributes. The counts
// are the point: a root that is configured and finds nothing looks identical to one that was never
// added, unless the number is on screen.
func llmsRoots() {
	roots := loadSessionRoots()
	counts := map[string]int{}
	for _, s := range collectSessions() {
		key := s.root
		if key == "" {
			if self, _ := os.UserHomeDir(); self != "" {
				key = self
			}
		}
		counts[key]++
	}

	fmt.Println("Where ptln looks for AI sessions:")
	for _, r := range roots {
		tag := "adopted"
		if r.Primary {
			tag = "this account"
		}
		fmt.Printf("  %-40s %-13s %d %s\n", shortHome(r.Home), tag, counts[r.Home], plural(counts[r.Home], "session", "sessions"))
	}
	fmt.Println()
	fmt.Println("Add another with `ptln llms --look-in <dir>` — pass the HOME a session ran under.")
	if len(roots) == 1 {
		if found := candidateRoots(); len(found) > 0 {
			fmt.Println()
			fmt.Println("Other homes on this machine that DO hold sessions:")
			for _, f := range found {
				fmt.Printf("  %s\n", shortHome(f))
			}
		}
	}
}

// adoptedRootsLine summarises the adopted roots for `ptln doctor`, or "" when there are none to
// report. Kept here so the doctor line and the command can never describe roots differently.
func adoptedRootsLine() string {
	var adopted []string
	for _, r := range loadSessionRoots() {
		if !r.Primary {
			adopted = append(adopted, shortHome(r.Home))
		}
	}
	if len(adopted) == 0 {
		return ""
	}
	return strings.Join(adopted, ", ")
}
