package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// wtMenu is the `ctrl-\ w` overlay: worktrees for the FOCUSED session — fork a parallel
// agent, or list/remove the repo's worktrees (menu parity for `ptln wt`). Removal is guarded
// by gitwt (never the repo itself).
func wtMenu(mx sessionMenuTarget) {
	_, dir, _, _, ok := mx.ActiveLaunch()
	if !ok || dir == "" {
		dir, _ = os.Getwd()
	}
	repo, rerr := gitwt.RepoRoot(dir)
	if rerr != nil {
		noteForm("Worktrees", []string{"this session isn't inside a git repository — no worktrees here."})
		return
	}
	m := formState(runForm(worktreeForm()))
	switch m.key {
	case 'f':
		wtForkFlow(mx, dir)
	case 'l':
		wtListFlow(repo)
	case 'r':
		wtRemoveFlow(repo)
	}
}

// wtForkFlow forks the FOCUSED session into a new worktree, opened as a NEW tab
// (SetPendingOpen), branched from the session's HEAD, inheriting its thread + MCP set.
func wtForkFlow(mx sessionMenuTarget, dir string) {
	argv, _, _, _, ok := mx.ActiveLaunch()
	if !ok || len(argv) == 0 {
		noteForm("Fork into worktree", []string{"open an AI session first — forking clones the session you're looking at."})
		return
	}
	bin := argv[0]
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		noteForm("Fork into worktree", []string{"not inside a git repository — nothing to fork."})
		return
	}

	forkBody := []string{
		fmt.Sprintf("session: %s in %s", bin, dir),
		"an isolated checkout — same history, own dir, own branch",
	}
	// This menu opens on a chord, so an accidental launch must have a zero-damage exit at every
	// step — askForm/confirmForm both treat esc and a lone q as cancel ("q" never becomes a wt name).
	name, ok := askForm("Fork into worktree", forkBody, "worktree name", "")
	if !ok {
		return
	}

	withState, ok := confirmForm("Fork into worktree", append(forkBody, "", "⎇ "+name),
		"carry your uncommitted work into it?", false)
	if !ok {
		return
	}

	// Fork from where THIS session is: its HEAD is the base, not origin/<default>.
	base := ""
	if out, e := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); e == nil {
		base = strings.TrimSpace(string(out))
	}
	wtPath, branch, err := gitwt.CreateFrom(repo, name, base)
	if err != nil {
		noteForm("Fork into worktree", []string{fmt.Sprintf("  ✗ %v", err)})
		return
	}
	_ = gitwt.SeedInclude(repo, wtPath) // .env/.mcp.json etc — best-effort
	if withState {
		if err := gitwt.MaterializeWIP(dir, wtPath); err != nil {
			noteForm("Fork into worktree", []string{
				fmt.Sprintf("  ✗ carrying state: %v", err),
				fmt.Sprintf("  (the worktree exists without it — %s)", wtPath)})
			return
		}
	}

	// The fork inherits this session's context: same thread (wired), same MCP servers —
	// isolated files, ONE shared memory. Fresh conversation (a new dir has no history).
	thread, _ := mx.ActiveThreadInfo()
	mcps := mx.ActiveMCPs()
	wiredArgv, eng := wireSessionArgv(bin, []string{bin}, thread, mcps)
	threadLabel := ""
	if thread != "" && api.LoadToken() != "" {
		if th, _, e := api.New().GetThread(thread); e == nil && th != nil {
			threadLabel = th.Title
		}
	}
	mx.SetPendingOpen(prepareLaunchSpec(Spec{
		Label: bin + " ⎇" + branch,
		Key:   fmt.Sprintf("wt-%s-%d", branch, time.Now().UnixNano()),
		Argv:  wiredArgv, Dir: wtPath,
		Thread: thread, ThreadLabel: threadLabel, Engine: eng, MCPs: mcps,
	}))
	done := []string{fmt.Sprintf("  ✓ %s — opening a new tab", wtPath)}
	if thread != "" {
		done = append(done, "  (same shared memory: both agents recall + record one thread)")
	}
	noteForm("Fork into worktree", done)
}

// wtListFlow prints the repo's linked worktrees (branch + path).
func wtListFlow(repo string) {
	ls, err := gitwt.List(repo)
	if err != nil {
		noteForm("Worktrees", []string{fmt.Sprintf("✗ %v", err)})
		return
	}
	var lines []string
	if len(ls) == 0 {
		lines = append(lines, "no worktrees — f forks one")
	}
	for _, w := range ls {
		lines = append(lines, fmt.Sprintf("⎇ %-24s %s", w[1], w[0]))
	}
	noteForm("Worktrees", lines)
}

// wtRemoveFlow lists worktrees, removes the picked one via the gitwt guard (never the repo).
func wtRemoveFlow(repo string) {
	ls, err := gitwt.List(repo)
	if err != nil || len(ls) == 0 {
		noteForm("Remove a worktree", []string{"no worktrees to remove"})
		return
	}
	items := make([]string, 0, len(ls))
	for _, w := range ls {
		items = append(items, "⎇ "+w[1])
	}
	warn := []string{"the directory goes away; the branch survives"}
	n, _, ok := formModel{kind: formPick, title: "Remove a worktree", body: warn, items: items, label: "remove"}.run()
	if !ok {
		return
	}
	if yes, cok := confirmForm("Remove a worktree", append(warn, "", "  "+ls[n][0]),
		"delete the worktree directory?", false); !cok || !yes {
		return
	}
	if err := gitwt.Remove(repo, ls[n][0]); err != nil {
		noteForm("Remove a worktree", []string{fmt.Sprintf("  ✗ %v", err)})
		return
	}
	noteForm("Remove a worktree", []string{
		fmt.Sprintf("  ✓ removed ⎇ %s", ls[n][1]),
		"  " + "(the branch survives — delete it with git if you're done)"})
}
