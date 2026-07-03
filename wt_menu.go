package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/ptymux"
)

// wtForkMenu is the `ctrl-\ w` overlay: fork the FOCUSED session into a git worktree —
// a parallel agent on the same repo, isolated files, same shared memory. Three keystrokes:
// a name, an optional "carry my uncommitted work", done. The new session opens as a NEW
// tab (SetPendingOpen), branched from the current session's HEAD (not origin/<default> —
// you're forking what you're looking at), inheriting the session's thread + MCP set.
func wtForkMenu(mx *ptymux.Mux) {
	in := bufio.NewReader(os.Stdin)
	argv, dir, _, _, ok := mx.ActiveLaunch()
	cgFrame("Fork into a worktree")
	if !ok || len(argv) == 0 {
		fmt.Printf("  %sopen a session first — forking clones the session you're looking at.%s\n", cgDim, cgOff)
		pause(in)
		return
	}
	bin := argv[0]
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		fmt.Printf("  %sthis session isn't inside a git repository — nothing to fork.%s\n", cgDim, cgOff)
		pause(in)
		return
	}

	fmt.Printf("  session: %s%s%s in %s\n", cgBold, bin, cgOff, dir)
	fmt.Printf("  %sa worktree is an isolated checkout of your repo — same history, own directory, own branch%s\n\n", cgDim, cgOff)
	fmt.Printf("  worktree name %s(enter or q cancels — back to your session)%s › ", cgDim, cgOff)
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
