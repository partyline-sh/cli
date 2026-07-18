package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/gitwt"
)

// wtMain is `ptln wt` — the small management surface for session worktrees (E3).
// Creation happens at launch (`ptln new claude --worktree <name>`); this lists what
// exists and removes safely (every removal goes through gitwt's linked-worktree guard,
// so the repo itself can never be deleted).
func wtMain(args []string) {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	dir, _ := os.Getwd()
	switch sub {
	case "", "ls", "list":
		repo, err := gitwt.RepoRoot(dir)
		if err != nil {
			fatal(err)
		}
		ls, err := gitwt.List(repo)
		if err != nil {
			fatal(err)
		}
		if len(ls) == 0 {
			fmt.Println("no worktrees — start one: ptln new claude --worktree <name>")
			return
		}
		fmt.Printf("%-30s  %s\n", "BRANCH", "PATH")
		for _, w := range ls {
			fmt.Printf("%-30s  %s\n", w[1], w[0])
		}
	case "rm", "remove":
		if len(args) < 1 {
			fatal(fmt.Errorf("usage: ptln wt rm <branch|path>"))
		}
		repo, err := gitwt.RepoRoot(dir)
		if err != nil {
			fatal(err)
		}
		target := args[0]
		path := ""
		if ls, e := gitwt.List(repo); e == nil {
			for _, w := range ls {
				if w[1] == target || w[0] == target {
					path = w[0]
					break
				}
			}
		}
		if path == "" {
			if abs, e := filepath.Abs(target); e == nil {
				path = abs
			}
		}
		if err := gitwt.Remove(repo, path); err != nil {
			fatal(err)
		}
		fmt.Printf("✓ removed %s (branch survives — delete it with git if you're done)\n", path)
	case "-h", "--help", "help":
		fmt.Println("ptln wt — session worktrees (isolated dirs for parallel agents)")
		fmt.Println("  ptln new <tool> --worktree <name>   start a session in its own worktree")
		fmt.Println("           add --thread <id> and parallel agents share one context thread")
		fmt.Println("  ptln wt                             list this repo's worktrees")
		fmt.Println("  ptln wt rm <branch|path>            remove one (guarded — never the repo)")
		fmt.Println("  seed gitignored config into new worktrees: list it in .worktreeinclude")
	default:
		fatal(fmt.Errorf("unknown: ptln wt %q — run `ptln wt help`", strings.TrimSpace(sub)))
	}
}
