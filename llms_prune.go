// `ptln llms prune` — drop sessions whose git worktree no longer exists from the
// switchboard's list.
//
// HARD SAFETY RULE: this NEVER touches disk state that isn't ours. It does not run
// `git worktree remove`, does not rm a directory, does not delete a tool's session
// store. Pruning means setting the same `archived` flag the switchboard's `x` key sets
// (in ~/.partyline/llms-meta.json), so the session drops out of the list and can be
// brought back with `a`/`x`. Real worktree teardown is deliberately out of scope —
// it's tracked separately as issue #641.
package main

import (
	"fmt"
	"os"
)

func llmsPrune(args []string) {
	apply := false
	for _, a := range args {
		switch a {
		case "--apply", "--yes", "-y":
			apply = true
		case "-h", "--help":
			fmt.Println("ptln llms prune — drop sessions whose git worktree is gone from the list")
			fmt.Println("  ptln llms prune          report what would be pruned (dry run — changes nothing)")
			fmt.Println("  ptln llms prune --apply  actually drop them from the list")
			fmt.Println("  Nothing is deleted from disk: no `git worktree remove`, no rm, no session files")
			fmt.Println("  touched. Pruned sessions are archived — `ptln llms ls --all` still lists them.")
			return
		default:
			fatal(fmt.Errorf("unknown flag %q — usage: ptln llms prune [--apply]", a))
		}
	}
	meta := loadLLMMeta()
	var dead []aiSession
	for _, s := range collectSessions() {
		if s.DeadWt && !meta[s.ID].Archived {
			dead = append(dead, s)
		}
	}
	if len(dead) == 0 {
		fmt.Println("nothing to prune — no un-pruned session is stranded in a deleted git worktree")
		return
	}
	fmt.Printf("%d session(s) whose git worktree no longer exists:\n\n", len(dead))
	fmt.Printf("  %-7s  %-9s  %-28s  %s\n", "TOOL", "ID", "PROJECT ⤷ WORKTREE", "TITLE")
	for _, s := range dead {
		where := projLabel(s.projKey()) + " ⤷ " + s.WtName
		fmt.Printf("  %-7s  %-9s  %-28s  %s\n",
			toolLabel(s.Tool), short(s.ID, 8), trunc(where, 28), trunc(s.Title, 44))
	}
	if !apply {
		fmt.Println("\ndry run — nothing changed. `ptln llms prune --apply` drops them from the list.")
		fmt.Println("(prune only hides sessions; it never deletes a worktree, a repo, or a session file.)")
		return
	}
	for _, s := range dead {
		mt := meta[s.ID]
		mt.Archived = true
		meta[s.ID] = mt
	}
	saveLLMMeta(meta)
	fmt.Fprintf(os.Stdout, "✓ pruned %d session(s) from the list — nothing was deleted from disk\n", len(dead))
	fmt.Println("  `ptln llms ls --all` still shows them; `x` in the switchboard un-archives one")
}
