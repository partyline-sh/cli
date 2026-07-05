package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/ptymux"
)

// wtMenu is the `ctrl-\ w` overlay: worktrees for the FOCUSED session — fork a parallel
// agent, or list/remove the repo's worktrees (menu parity for `ptln wt`). Removal is guarded
// by gitwt (never the repo itself).
func wtMenu(mx *ptymux.Mux) {
	in := bufio.NewReader(os.Stdin)
	_, dir, _, _, ok := mx.ActiveLaunch()
	if !ok || dir == "" {
		dir, _ = os.Getwd()
	}
	repo, rerr := gitwt.RepoRoot(dir)
	if rerr != nil {
		cgBox("Worktrees", []string{fmt.Sprintf("%sthis session isn't inside a git repository — no worktrees here.%s", cgDim, cgOff)})
		pause(in)
		return
	}
	cgBox("Worktrees", []string{
		cgRow("f", "fork this session into a worktree", "parallel agent, shared memory"),
		cgRow("l", "list this repo's worktrees", ""),
		cgRow("r", "remove a worktree", "guarded — never the repo"),
		cgRow("q", "back to your session", ""),
	})
	fmt.Print("  › ")
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "f", "":
		if strings.TrimSpace(line) == "" {
			return // bare enter = cancel
		}
		wtForkFlow(mx, in, dir)
	case "l":
		wtListFlow(in, repo)
	case "r":
		wtRemoveFlow(in, repo)
	}
}

// wtForkFlow forks the FOCUSED session into a new worktree, opened as a NEW tab
// (SetPendingOpen), branched from the session's HEAD, inheriting its thread + MCP set.
func wtForkFlow(mx *ptymux.Mux, in *bufio.Reader, dir string) {
	argv, _, _, _, ok := mx.ActiveLaunch()
	if !ok || len(argv) == 0 {
		cgBox("Fork into worktree", []string{fmt.Sprintf("%sopen an AI session first — forking clones the session you're looking at.%s", cgDim, cgOff)})
		pause(in)
		return
	}
	bin := argv[0]
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		cgBox("Fork into worktree", []string{fmt.Sprintf("%snot inside a git repository — nothing to fork.%s", cgDim, cgOff)})
		pause(in)
		return
	}

	cgBox("Fork into worktree", []string{
		fmt.Sprintf("session: %s%s%s in %s", cgBold, bin, cgOff, dir),
		fmt.Sprintf("%san isolated checkout — same history, own dir, own branch%s", cgDim, cgOff),
	})
	fmt.Printf("  worktree name %s(enter or q cancels)%s › ", cgDim, cgOff)
	name, _ := in.ReadString('\n')
	name = strings.TrimSpace(name)
	// Bail on empty OR a lone q — this menu opens on a chord, so an accidental launch must
	// have a zero-damage exit (and "q" must never become a worktree's name).
	if name == "" || strings.EqualFold(name, "q") {
		return
	}

	fmt.Printf("  carry your uncommitted work into it? %s(y/N · q cancels)%s › ", cgDim, cgOff)
	ws, _ := in.ReadString('\n')
	wsl := strings.ToLower(strings.TrimSpace(ws))
	if wsl == "q" {
		return
	}
	withState := strings.HasPrefix(wsl, "y")

	// Fork from where THIS session is: its HEAD is the base, not origin/<default>.
	base := ""
	if out, e := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); e == nil {
		base = strings.TrimSpace(string(out))
	}
	wtPath, branch, err := gitwt.CreateFrom(repo, name, base)
	if err != nil {
		fmt.Printf("  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return
	}
	_ = gitwt.SeedInclude(repo, wtPath) // .env/.mcp.json etc — best-effort
	if withState {
		if err := gitwt.MaterializeWIP(dir, wtPath); err != nil {
			fmt.Printf("  %s✗ carrying state: %v%s\n", cgBad, err, cgOff)
			fmt.Printf("  %s(the worktree exists without it — %s)%s\n", cgDim, wtPath, cgOff)
			pause(in)
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
	fmt.Printf("\n  %s✓%s %s%s%s — opening a new tab", cgOK, cgOff, cgBold, wtPath, cgOff)
	if thread != "" {
		fmt.Printf(" %s(same shared memory: both agents recall + record one thread)%s", cgDim, cgOff)
	}
	fmt.Print("\n")
	pause(in)
}

// wtListFlow prints the repo's linked worktrees (branch + path).
func wtListFlow(in *bufio.Reader, repo string) {
	ls, err := gitwt.List(repo)
	if err != nil {
		cgBox("Worktrees", []string{fmt.Sprintf("%s✗ %v%s", cgBad, err, cgOff)})
		pause(in)
		return
	}
	var lines []string
	if len(ls) == 0 {
		lines = append(lines, fmt.Sprintf("%sno worktrees — f forks one%s", cgDim, cgOff))
	}
	for _, w := range ls {
		lines = append(lines, fmt.Sprintf("%s⎇ %-24s%s %s%s%s", cgWire, w[1], cgOff, cgDim, w[0], cgOff))
	}
	cgBox("Worktrees", lines)
	pause(in)
}

// wtRemoveFlow lists worktrees, removes the picked one via the gitwt guard (never the repo).
func wtRemoveFlow(in *bufio.Reader, repo string) {
	ls, err := gitwt.List(repo)
	if err != nil || len(ls) == 0 {
		cgBox("Remove a worktree", []string{fmt.Sprintf("%sno worktrees to remove%s", cgDim, cgOff)})
		pause(in)
		return
	}
	var lines []string
	for i, w := range ls {
		lines = append(lines, fmt.Sprintf("    %s%d%s  ⎇ %s", cgKey, i+1, cgOff, w[1]))
	}
	cgBox("Remove a worktree", lines)
	fmt.Printf("  › number to remove %s(enter cancels)%s: ", cgDim, cgOff)
	s, _ := in.ReadString('\n')
	n, e := strconv.Atoi(strings.TrimSpace(s))
	if e != nil || n < 1 || n > len(ls) {
		return
	}
	if err := gitwt.Remove(repo, ls[n-1][0]); err != nil {
		fmt.Printf("  %s✗ %v%s\n", cgBad, err, cgOff)
	} else {
		fmt.Printf("  %s✓%s removed ⎇ %s (branch survives — delete with git if done)\n", cgOK, cgOff, ls[n-1][1])
	}
	pause(in)
}
