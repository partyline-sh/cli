package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// A repo binds itself to its Context Thread with a `.partyline.json` at the repo root
// (E3.5). It's meant to be CHECKED IN — the repo itself declares where its shared memory
// lives, so every teammate (and every parallel worktree agent) who launches a session in
// this repo auto-attaches to the same thread. Opt out per launch with --no-thread.
type repoBind struct {
	Thread string `json:"thread,omitempty"`
}

func repoBindPath(repo string) string { return filepath.Join(repo, ".partyline.json") }

// loadRepoBind returns the thread bound to dir's repository ("" when none / not a repo).
func loadRepoBind(dir string) string {
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(repoBindPath(repo))
	if err != nil {
		return ""
	}
	var rb repoBind
	if json.Unmarshal(b, &rb) != nil {
		return ""
	}
	return strings.TrimSpace(rb.Thread)
}

// threadBind is `ptln thread bind [<id> | --clear]`: bind the current repo to a thread
// (no args shows the current binding).
func threadBind(c *api.Client, args []string) {
	dir, _ := os.Getwd()
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		fatal(fmt.Errorf("thread bind works inside a git repository: %w", err))
	}
	p := repoBindPath(repo)
	if len(args) == 0 {
		if id := loadRepoBind(dir); id != "" {
			title := id
			if th, _, e := c.GetThread(id); e == nil && th != nil {
				title = th.Title
			}
			fmt.Printf("bound to: %s (%s)\n  every `ptln new` in this repo auto-attaches (--no-thread skips)\n", title, id)
		} else {
			fmt.Println("no binding — `ptln thread bind <id>` writes .partyline.json (check it in to share with the team)")
		}
		return
	}
	if args[0] == "--clear" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fatal(err)
		}
		fmt.Println("✓ binding cleared")
		return
	}
	id := strings.TrimSpace(args[0])
	th, _, err := c.GetThread(id)
	if err != nil || th == nil {
		fatal(fmt.Errorf("can't read thread %s: %v", id, err))
	}
	if err := writeRepoBind(repo, id); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ %s bound to %q — check .partyline.json in so the whole team's agents share it\n", filepath.Base(repo), th.Title)
}

// writeRepoBind persists the repo→thread binding (shared by the CLI and the ctrl-\ c menu).
func writeRepoBind(repo, thread string) error {
	b, _ := json.MarshalIndent(repoBind{Thread: thread}, "", "  ")
	return os.WriteFile(repoBindPath(repo), append(b, '\n'), 0o644)
}
