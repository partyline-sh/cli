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
		args, yes := takeYesFlag(args)
		if len(args) < 1 {
			fatal(fmt.Errorf("usage: ptln wt rm <branch|path> [--yes]"))
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
		if !confirmDestructive("delete the worktree directory "+path, yes) {
			return
		}
		if err := gitwt.Remove(repo, path); err != nil {
			fatal(err)
		}
		fmt.Printf("✓ removed %s (branch survives — delete it with git if you're done)\n", path)
	case "prune":
		// #641's reaper for the backlog that accumulated BEFORE crank learned to clean up after
		// itself — 35 live worktrees on one machine, 21 of 66 branches theirs.
		//
		// DRY RUN BY DEFAULT, and that is not politeness. One of the orphans found during the
		// manual cleanup held 23 uncommitted files; a reaper that deleted first would have
		// destroyed real work with no way back. You see the list, then you opt in.
		_, yes := takeYesFlag(args)
		repo, err := gitwt.RepoRoot(dir)
		if err != nil {
			fatal(err)
		}
		ls, err := gitwt.List(repo)
		if err != nil {
			fatal(err)
		}
		var removable, kept [][2]string // [path, why]
		for _, w := range ls {
			path, branch := w[0], w[1]
			// Only crank's own worktrees. A worktree someone made by hand for their own work is
			// not this command's business, and guessing wrong deletes a person's workspace.
			if !strings.HasPrefix(branch, "crank-") {
				continue
			}
			if gitwt.Dirty(path) {
				kept = append(kept, [2]string{path, "uncommitted changes"})
				continue
			}
			removable = append(removable, [2]string{path, branch})
		}
		if len(removable) == 0 && len(kept) == 0 {
			fmt.Println("no crank worktrees to prune.")
			return
		}
		for _, k := range kept {
			fmt.Printf("  keep    %s  (%s)\n", k[0], k[1])
		}
		for _, r := range removable {
			fmt.Printf("  remove  %s  (branch %s survives)\n", r[0], r[1])
		}
		if len(removable) == 0 {
			fmt.Println("\nnothing to remove — everything left has uncommitted work in it.")
			return
		}
		if !yes {
			fmt.Printf("\n%d worktree(s) would be removed. Branches are never touched — the commits survive.\n", len(removable))
			fmt.Println("Re-run with --yes to do it.")
			return
		}
		for _, r := range removable {
			if err := gitwt.Remove(repo, r[0]); err != nil {
				fmt.Printf("  ✗ %s: %v\n", r[0], err)
				continue
			}
			fmt.Printf("  ✓ removed %s\n", r[0])
		}
	case "-h", "--help", "help":
		fmt.Println("ptln wt — session worktrees (isolated dirs for parallel agents)")
		fmt.Println("  ptln new <tool> --worktree <name>   start a session in its own worktree")
		fmt.Println("           add --thread <id> and parallel agents share one context thread")
		fmt.Println("  ptln wt                             list this repo's worktrees")
		fmt.Println("  ptln wt rm <branch|path>            remove one (asks first; --yes to skip)")
		fmt.Println("  ptln wt prune                       clear finished crank worktrees (dry run; --yes to apply)")
		fmt.Println("           never touches branches, and never a worktree with uncommitted work")
		fmt.Println("  seed gitignored config into new worktrees: list it in .worktreeinclude")
	default:
		fatal(fmt.Errorf("unknown: ptln wt %q — run `ptln wt help`", strings.TrimSpace(sub)))
	}
}
