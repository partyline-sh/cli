// `ptln llms prune` — delete sessions whose git worktree no longer exists.
//
// This used to hide them instead, by setting an `archived` flag in ~/.partyline/llms-meta.json,
// because partyline would not touch disk state it does not own. Archive is gone: hiding a session
// and deleting one were indistinguishable at the moment you pressed the key, and the list only
// ever grew. Pruning now does what its name says.
//
// The safety rule survives in the mechanism rather than the outcome: prune moves files into
// ~/.partyline/trash through the same deleteSession path the switchboard's `x` uses, with a
// manifest per entry recording where each came from. Nothing is unlinked, so a prune that took
// more than you meant is recoverable. It still never runs `git worktree remove` and never rm's a
// repo — only the engine's own session files move.
//
// A LIVE session is skipped. Its engine is writing to that store right now.
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
			fmt.Println("ptln llms prune — delete sessions whose git worktree is gone")
			fmt.Println("  ptln llms prune          report what would be deleted (dry run — changes nothing)")
			fmt.Println("  ptln llms prune --apply  delete them")
			fmt.Println("  Their session files MOVE to ~/.partyline/trash with a manifest recording where")
			fmt.Println("  each came from, so this is recoverable. No `git worktree remove`, no rm of a repo.")
			fmt.Println("  Running sessions are skipped.")
			return
		default:
			fatal(fmt.Errorf("unknown flag %q — usage: ptln llms prune [--apply]", a))
		}
	}
	meta := loadLLMMeta()
	var dead, live []aiSession
	for _, s := range collectSessions() {
		if !s.DeadWt {
			continue
		}
		if s.Live {
			live = append(live, s) // its engine is mid-turn; not ours to move
			continue
		}
		if _, ok := sessionFiles(s); !ok {
			continue // no file per session (llm's shared log) — nothing to move
		}
		dead = append(dead, s)
	}
	if len(live) > 0 {
		fmt.Printf("skipping %d running session(s) whose worktree is gone — close them first\n\n", len(live))
	}
	if len(dead) == 0 {
		fmt.Println("nothing to prune — no session is stranded in a deleted git worktree")
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
		fmt.Println("\ndry run — nothing changed. `ptln llms prune --apply` deletes them.")
		fmt.Println("(their files move to ~/.partyline/trash; no worktree or repo is touched.)")
		return
	}
	done, failed := 0, 0
	for _, s := range dead {
		if _, err := deleteSession(s, meta); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", short(s.ID, 8), err)
			failed++
			continue
		}
		done++
	}
	fmt.Fprintf(os.Stdout, "✓ deleted %d session(s) — recoverable from %s\n", done, sessionTrashDir())
	if failed > 0 {
		fmt.Fprintf(os.Stdout, "  %d could not be moved and are untouched\n", failed)
	}
}
