package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Session recovery. A session's transcript lives in the tool's store (not in the working
// dir), so a session survives its directory being deleted — but you can't resume it
// because there's no cwd to resume into. Two halves:
//   - CAPTURE: while a dir is alive, record its git origin (sessMeta.Repo) so we know
//     where the code came from. Once the dir is gone it's too late to read it.
//   - RECOVER: on ↵ over a gone-dir session, a framed modal offers to recreate the dir
//     (clone the recorded remote, clone a remote you paste, or an empty dir) and resume
//     the original conversation there. The modal has a clean cancel — nothing happens
//     unless you choose it.

// sessRepo reads a session's git origin URL from its (still-live) cwd. Best-effort,
// bounded, and NETWORK-FREE (reads .git/config). "" if not a git repo / no origin.
func sessRepo(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return stripURLCreds(strings.TrimSpace(string(out)))
}

// stripURLCreds removes embedded credentials from an https remote
// (https://user:token@host/… → https://host/…) so a token in the origin URL never lands
// in the persisted sidecar (guardrail #110). scp-style ssh remotes (git@host:path) carry
// no secret and pass through untouched.
func stripURLCreds(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u // scp-style (git@host:path) or bare — no scheme authority to strip
	}
	// Only http/https userinfo is a credential. An ssh:// userinfo (git@host) is the
	// LOGIN user — stripping it would break the clone — so leave non-http schemes alone.
	if sc := strings.ToLower(u[:i]); sc != "http" && sc != "https" {
		return u
	}
	scheme, rest := u[:i+3], u[i+3:]
	slash := strings.IndexByte(rest, '/')
	at := strings.IndexByte(rest, '@')
	if at >= 0 && (slash < 0 || at < slash) { // '@' is inside the authority → strip user[:pass]
		rest = rest[at+1:]
	}
	return scheme + rest
}

// captureSessionRepos records the git origin for every session whose cwd STILL EXISTS
// but hasn't been looked at yet — the info can't be read once the dir is gone. The
// RepoChecked marker means each session is inspected exactly ONCE ever (repo or not),
// so later launches never re-fork git for it. Returns whether anything changed so the
// caller persists once. Runs off the UI path (a background goroutine).
func captureSessionRepos(all []aiSession, meta map[string]sessMeta) bool {
	changed := false
	for _, s := range all {
		mt := meta[s.ID]
		if s.Cwd == "" || mt.Repo != "" || mt.RepoChecked {
			continue // no dir, already captured, or already looked — never re-fork git
		}
		if _, err := os.Stat(s.Cwd); err != nil {
			continue // gone/unreadable — cheap stat, no fork; may capture if it returns
		}
		mt.Repo = sessRepo(s.Cwd) // "" if the cwd isn't a git repo
		mt.RepoChecked = true     // looked once → this session never git-forks again
		meta[s.ID] = mt
		changed = true
	}
	return changed
}

// recoverModal returns the suspend closure for recovering a gone-dir session. It runs in
// the mux's cooked overlay (same as the ctrl-\ menus), draws a framed modal, and does
// NOTHING unless you pick an action — q / enter / anything unrecognised cancels back to
// the switchboard (the exit the old suspend-print flow was missing). capturedRepo is the
// origin we snapshotted while the dir was alive ("" for legacy/never-git sessions).
func recoverModal(s aiSession, capturedRepo string) func() {
	return func() {
		in := bufio.NewReader(os.Stdin)
		dir := s.Cwd
		lines := []string{
			cgDim + "its working directory was removed:" + cgOff,
			"  " + cgBad + dir + cgOff,
			"",
			cgDim + "the conversation survived — only the directory is gone." + cgOff,
			"",
		}
		if capturedRepo != "" {
			lines = append(lines,
				"recorded remote: "+cgOK+capturedRepo+cgOff, "",
				cgRow("1", "clone & resume", "git clone the recorded remote"))
		} else {
			lines = append(lines, cgDim+"no remote was recorded for this session."+cgOff, "")
		}
		lines = append(lines,
			cgRow("r", "clone a remote…", "paste a git URL to clone"),
			cgRow("e", "empty dir & resume", "recreate the directory empty"),
			cgRow("q", "cancel", "↩ back to the switchboard"))
		cgBox("Recover session", lines)
		fmt.Print("  › ")
		line, _ := in.ReadString('\n')

		repo := ""
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "1":
			if capturedRepo == "" {
				return // '1' wasn't offered → treat as cancel
			}
			repo = capturedRepo
		case "r":
			fmt.Print("\n  git remote URL › ")
			u, _ := in.ReadString('\n')
			if repo = strings.TrimSpace(u); repo == "" {
				return
			}
		case "e":
			repo = "" // empty dir
		default:
			return // q / enter / unrecognised → clean cancel
		}

		if !recreateDir(in, dir, repo) {
			return // failed (already explained + paused) — back to the switchboard
		}

		bin, err := exec.LookPath(s.resumeArgv[0])
		if err != nil {
			fmt.Printf("\n  %s✗ %s not found on PATH.%s\n", cgBad, s.resumeArgv[0], cgOff)
			pause(in)
			return
		}
		fmt.Printf("\n  %s✓%s restored — resuming the conversation…\n", cgOK, cgOff)
		c := exec.Command(bin, s.resumeArgv[1:]...)
		c.Dir = dir
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = c.Run() // on exit the mux re-enters and shows the switchboard
	}
}

// recreateDir clones repo into dir (or makes an empty dir when repo==""). Returns false
// on failure, after printing the reason and pausing so the user can read it.
func recreateDir(in *bufio.Reader, dir, repo string) bool {
	if repo == "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Printf("\n  %s✗ couldn't recreate %s: %v%s\n", cgBad, dir, err, cgOff)
			pause(in)
			return false
		}
		fmt.Printf("\n  %s✓%s recreated %s (empty — add your code when ready).\n", cgOK, cgOff, dir)
		return true
	}
	_ = os.MkdirAll(filepath.Dir(dir), 0o755) // clone needs the parent to exist; the leaf must not
	fmt.Printf("\n  %scloning %s → %s …%s\n\n", cgDim, repo, dir, cgOff)
	c := exec.Command("git", "clone", repo, dir)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Printf("\n  %s✗ clone failed: %v%s\n", cgBad, err, cgOff)
		pause(in)
		return false
	}
	return true
}
