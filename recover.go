package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
			cgRow("d", "point to new location…", "you MOVED the dir — resume from its new path"),
			cgRow("r", "clone a remote…", "paste a git URL to clone"),
			cgRow("e", "empty dir & resume", "recreate the directory empty"),
			cgRow("q", "cancel", "or esc · ↩ back to the switchboard"))
		cgBox("Recover session", lines)

		repo := ""
		relocated := false
		switch menuKey() {
		case '1':
			if capturedRepo == "" {
				return // '1' wasn't offered → treat as cancel
			}
			repo = capturedRepo
		case 'd':
			// The dir MOVED, not gone. Point at the new path, remember it (so gone-detection +
			// resume use it from now on — the tool's own store can't be rewritten), and resume
			// there directly — no clone, no recreate, the real work is intact.
			nd := relocateSession(in, s)
			if nd == "" {
				return // empty / not a dir / unmigratable tool (already explained + paused) → cancel
			}
			dir, relocated = nd, true
		case 'r':
			fmt.Print("\n  git remote URL › ")
			u, _ := in.ReadString('\n')
			if repo = strings.TrimSpace(u); repo == "" {
				return
			}
		case 'e':
			repo = "" // empty dir
		default:
			return // q / esc / enter / unrecognised → clean cancel
		}

		// Relocation already has a live dir; only the clone/empty paths create one.
		if !relocated && !recreateDir(in, dir, repo) {
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

// relocateSession handles the "I moved the dir" case: prompt for the new path, validate it's a
// real directory, MIGRATE the tool's session store so the tool can actually resume there (the
// store is keyed by cwd — see migrateSessionStore), and persist the new path as this session's
// CwdOverride so gone-detection + resume use it from now on (applyCwdOverrides at load). Returns
// the resolved new path, or "" on cancel / invalid input / a tool we can't migrate (after
// explaining + pausing). NETWORK-FREE, no clone — the conversation + code are intact.
func relocateSession(in *bufio.Reader, s aiSession) string {
	fmt.Print("\n  new directory path › ")
	line, _ := in.ReadString('\n')
	p := strings.TrimSpace(line)
	if p == "" {
		return "" // cancel
	}
	// Expand a leading ~ and make it absolute so the stored override is unambiguous later.
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		fmt.Printf("\n  %s✗ not a directory: %s%s\n", cgBad, p, cgOff)
		pause(in)
		return ""
	}
	// The tool files its session store by working directory, so resuming in the new path finds
	// nothing until we migrate the store to match. Do that BEFORE persisting anything — if the
	// tool isn't one we can migrate, say so honestly and don't leave a half-relocated state.
	if err := migrateSessionStore(s, p); err != nil {
		fmt.Printf("\n  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return ""
	}
	// Persist the override (merge into a fresh load so we don't clobber a concurrent write).
	meta := loadLLMMeta()
	mt := meta[s.ID]
	mt.CwdOverride = p
	meta[s.ID] = mt
	saveLLMMeta(meta)
	fmt.Printf("\n  %s✓%s relocated → %s (store migrated; remembered for next time).\n", cgOK, cgOff, p)
	return p
}

// migrateSessionStore makes the tool's session resolvable from newDir, since these tools file (and
// resume) sessions BY working directory. Claude keys its store by an encoded cwd
// (~/.claude/projects/<encoded>/<id>.jsonl), so we copy the transcript into the new cwd's project
// dir (copy, not move — the original stays as a backup). Tools we haven't taught this to yet return
// an error rather than silently "relocating" into a session the tool can't find.
func migrateSessionStore(s aiSession, newDir string) error {
	switch s.Tool {
	case "claude":
		if s.storePath == "" {
			return fmt.Errorf("no store file recorded for this session")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		destDir := filepath.Join(home, ".claude", "projects", claudeProjectDir(newDir))
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return fmt.Errorf("create project dir: %w", err)
		}
		return copyFile(s.storePath, filepath.Join(destDir, filepath.Base(s.storePath)))
	default:
		return fmt.Errorf("relocation isn't supported for %s yet — its sessions are keyed by directory; resume it natively in the new dir", s.Tool)
	}
}

// claudeProjectDir is Claude Code's cwd → project-dir-name encoding: every non-alphanumeric
// character becomes '-' (so `/Users/x/.foo/bar` → `-Users-x--foo-bar`; consecutive separators are
// NOT collapsed). Verified against real ~/.claude/projects dir names.
func claudeProjectDir(cwd string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
}

// copyFile copies src → dst (0600, transcripts are private). Fails if dst already exists so a
// relocation never clobbers an existing session there.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil // already migrated (idempotent) — the tool can already find it there
		}
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
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
