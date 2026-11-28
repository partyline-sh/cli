// `ptln llms prune` — delete sessions stranded in a git worktree that no longer exists, or started
// in a temp folder (/tmp, /var/folders) that was never a real project.
//
// A missing project folder is deliberately NOT a reason. Sessions copied between machines record
// the other machine's path (/mnt/data/dev/… on a Mac), so "folder gone" would sweep up real,
// recent work — including a session open right now.
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
	"path/filepath"
	"strings"
)

// pruneReason says why prune would take a session, or "" to leave it.
func pruneReason(s aiSession) string {
	switch {
	case s.DeadWt:
		return "worktree gone"
	case s.Cwd != "" && isTempDir(s.Cwd):
		return "temp folder"
	}
	return ""
}

// isTempDir reports whether dir is the system temp area or under it.
func isTempDir(dir string) bool {
	dir = filepath.Clean(dir)
	roots := []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders", filepath.Clean(os.TempDir())}
	for _, r := range roots {
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func llmsPrune(args []string) {
	apply := false
	for _, a := range args {
		switch a {
		case "--apply", "--yes", "-y":
			apply = true
		case "-h", "--help":
			fmt.Println("ptln llms prune — delete sessions whose git worktree is gone, or that ran in a temp folder")
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
	reasons := map[string]string{}
	open := launcherLiveKeys() // open in tmux, even if idle: Live only means "wrote in the last 45s"
	for _, s := range collectSessions() {
		why := pruneReason(s)
		if why == "" {
			continue
		}
		if s.Live || open[s.ID] {
			live = append(live, s) // its engine is running; not ours to move
			continue
		}
		if _, ok := sessionFiles(s); !ok {
			continue // no file per session (llm's shared log) — nothing to move
		}
		reasons[s.Tool+":"+s.ID] = why
		dead = append(dead, s)
	}
	if len(live) > 0 {
		fmt.Printf("skipping %d running session(s) that would otherwise be pruned — close them first\n\n", len(live))
	}
	if len(dead) == 0 {
		fmt.Println("nothing to prune — no session is in a deleted git worktree or a temp folder")
		return
	}
	fmt.Printf("%d session(s) to prune:\n\n", len(dead))
	fmt.Printf("  %-7s  %-9s  %-13s  %-28s  %s\n", "TOOL", "ID", "WHY", "PROJECT", "TITLE")
	for _, s := range dead {
		where := projLabel(s.projKey())
		if s.WtName != "" {
			where += " ⤷ " + s.WtName
		}
		fmt.Printf("  %-7s  %-9s  %-13s  %-28s  %s\n",
			toolLabel(s.Tool), short(s.ID, 8), reasons[s.Tool+":"+s.ID], trunc(where, 28), trunc(s.Title, 44))
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
