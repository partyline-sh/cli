package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/ptymux"
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
		cgNote("Worktrees", []string{fmt.Sprintf("%sthis session isn't inside a git repository — no worktrees here.%s", cgDim, cgOff)})
		return
	}
	cgBox("Worktrees", []string{
		cgRow("f", "fork this session into a worktree", "parallel agent, shared memory"),
		cgRow("l", "list this repo's worktrees", ""),
		cgRow("r", "remove a worktree", "guarded — never the repo"),
		"",
		cgHintRow("WORKTREE"),
	})
	switch menuKey() { // single keypress; q / esc / enter cancel
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
		cgNote("Fork into worktree", []string{fmt.Sprintf("%sopen an AI session first — forking clones the session you're looking at.%s", cgDim, cgOff)})
		return
	}
	bin := argv[0]
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		cgNote("Fork into worktree", []string{fmt.Sprintf("%snot inside a git repository — nothing to fork.%s", cgDim, cgOff)})
		return
	}

	forkBody := []string{
		fmt.Sprintf("session: %s%s%s in %s", cgBold, bin, cgOff, dir),
		fmt.Sprintf("%san isolated checkout — same history, own dir, own branch%s", cgDim, cgOff),
	}
	// This menu opens on a chord, so an accidental launch must have a zero-damage exit at every
	// step — cgAsk/cgConfirm both treat esc and a lone q as cancel ("q" never becomes a wt name).
	name, ok := cgAsk("Fork into worktree", forkBody, "worktree name", "")
	if !ok {
		return
	}

	withState, ok := cgConfirm("Fork into worktree", append(forkBody, "", dim("⎇ "+name)),
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
		cgNote("Fork into worktree", []string{fmt.Sprintf("  %s✗ %v%s", cgBad, err, cgOff)})
		return
	}
	_ = gitwt.SeedInclude(repo, wtPath) // .env/.mcp.json etc — best-effort
	if withState {
		if err := gitwt.MaterializeWIP(dir, wtPath); err != nil {
			cgNote("Fork into worktree", []string{
				fmt.Sprintf("  %s✗ carrying state: %v%s", cgBad, err, cgOff),
				fmt.Sprintf("  %s(the worktree exists without it — %s)%s", cgDim, wtPath, cgOff)})
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
	mx.SetPendingOpen(ptymux.Spec{
		Label: bin + " ⎇" + branch,
		Key:   fmt.Sprintf("wt-%s-%d", branch, time.Now().UnixNano()),
		Argv:  wiredArgv, Dir: wtPath,
		Thread: thread, ThreadLabel: threadLabel, Engine: eng, MCPs: mcps,
	})
	done := []string{fmt.Sprintf("  %s✓%s %s%s%s — opening a new tab", cgOK, cgOff, cgBold, wtPath, cgOff)}
	if thread != "" {
		done = append(done, fmt.Sprintf("  %s(same shared memory: both agents recall + record one thread)%s", cgDim, cgOff))
	}
	cgNote("Fork into worktree", done)
}

// wtListFlow prints the repo's linked worktrees (branch + path).
func wtListFlow(repo string) {
	ls, err := gitwt.List(repo)
	if err != nil {
		cgNote("Worktrees", []string{fmt.Sprintf("%s✗ %v%s", cgBad, err, cgOff)})
		return
	}
	var lines []string
	if len(ls) == 0 {
		lines = append(lines, fmt.Sprintf("%sno worktrees — f forks one%s", cgDim, cgOff))
	}
	for _, w := range ls {
		lines = append(lines, fmt.Sprintf("%s⎇ %-24s%s %s%s%s", cgWire, w[1], cgOff, cgDim, w[0], cgOff))
	}
	cgNote("Worktrees", lines)
}

// wtRemoveFlow lists worktrees, removes the picked one via the gitwt guard (never the repo).
func wtRemoveFlow(repo string) {
	ls, err := gitwt.List(repo)
	if err != nil || len(ls) == 0 {
		cgNote("Remove a worktree", []string{fmt.Sprintf("%sno worktrees to remove%s", cgDim, cgOff)})
		return
	}
	items := make([]string, 0, len(ls))
	for _, w := range ls {
		items = append(items, "⎇ "+w[1])
	}
	warn := []string{dim("the directory goes away; the branch survives")}
	n, _, ok := cgPicker{Title: "Remove a worktree", Body: warn, Items: items, Verb: "remove"}.run()
	if !ok {
		return
	}
	if yes, cok := cgConfirm("Remove a worktree", append(warn, "", "  "+dim(ls[n][0])),
		"delete the worktree directory?", false); !cok || !yes {
		return
	}
	if err := gitwt.Remove(repo, ls[n][0]); err != nil {
		cgNote("Remove a worktree", []string{fmt.Sprintf("  %s✗ %v%s", cgBad, err, cgOff)})
		return
	}
	cgNote("Remove a worktree", []string{
		fmt.Sprintf("  %s✓%s removed ⎇ %s", cgOK, cgOff, ls[n][1]),
		"  " + dim("(the branch survives — delete it with git if you're done)")})
}
