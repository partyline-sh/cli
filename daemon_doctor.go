package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

// daemon_doctor.go — `ptln daemon doctor`: check every prerequisite a crank run needs and print
// pass/fail with the EXACT fix, BEFORE any tokens are spent. The failure this exists to catch: a run
// works for 19 minutes, commits, pushes — then `gh pr create` dies because the daemon's env can't see
// the repo. Doctor turns that (and every sibling gap: no login, missing git identity, no engine, no
// toolchain) into a red line up front. Best-effort and read-only — it never changes anything.

type checkStatus int

const (
	ckPass checkStatus = iota
	ckWarn
	ckFail
)

func (s checkStatus) glyph() string {
	switch s {
	case ckPass:
		return "✓"
	case ckWarn:
		return "⚠"
	default:
		return "✗"
	}
}

// line prints one check row: glyph, what was checked, and (on warn/fail) the fix.
func (s checkStatus) line(what, detail, fix string) {
	fmt.Printf("  %s %s", s.glyph(), what)
	if detail != "" {
		fmt.Printf(" — %s", detail)
	}
	fmt.Println()
	if s != ckPass && fix != "" {
		fmt.Printf("      → %s\n", fix)
	}
}

// runCmd runs a command (optionally in dir, optionally with an extra env pair) and returns trimmed
// combined output + ok. The seam for every external probe git/gh/engine doctor makes.
func runCmd(dir, envPair string, name string, args ...string) (string, bool) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if envPair != "" {
		cmd.Env = append(os.Environ(), envPair)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

func daemonDoctor() {
	fmt.Print("ptln daemon doctor — checking crank prerequisites\n\n")

	// ── Account & enrolment ──────────────────────────────────────────────────
	if api.LoadToken() != "" {
		ckPass.line("CLI logged in", "", "")
	} else {
		ckFail.line("CLI logged in", "no local token", "run `ptln login`")
	}

	dev := loadDaemonDevice()
	if dev.Token != "" {
		ckPass.line("daemon enrolled", dev.DaemonID, "")
	} else {
		ckFail.line("daemon enrolled", "this machine isn't enrolled", "run `ptln daemon enable`")
	}

	// ── GitHub PR access (the gap that bit us) ───────────────────────────────
	tok := resolveGitHubToken()
	if tok == "" {
		ckWarn.line("GitHub token", "none found (needed only for merge_policy pr/auto)",
			"run `gh auth login`, then `gh auth token | ptln daemon set-github-token`")
	} else {
		src := "stored / env / gh"
		if readStoredGitHubToken() != "" {
			src = "stored locally"
		}
		ckPass.line("GitHub token", "resolved ("+src+")", "")
	}

	// ── Toolchain (informational — build-preset tasks run tests with these) ───
	var haveTools []string
	for _, t := range []string{"git", "gh", "node", "npm", "go"} {
		if _, err := exec.LookPath(t); err == nil {
			haveTools = append(haveTools, t)
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		ckFail.line("git on PATH", "not found", "install git")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		ckWarn.line("gh (GitHub CLI) on PATH", "not found — needed for PR creation", "install gh (https://cli.github.com)")
	}
	fmt.Printf("  · toolchain present: %s\n", strings.Join(haveTools, " "))

	// ── Context MCP wiring (#558) — is recall/remember actually THERE, per engine ─
	doctorMCPWiring()

	// ── Per project ──────────────────────────────────────────────────────────
	reg := loadDaemonRegistry()
	if len(reg.Projects) == 0 {
		ckWarn.line("registered projects", "none", "register one with `ptln daemon add-project <label> [dir]`")
	}
	for _, p := range reg.Projects {
		fmt.Printf("\n  project %q — %s\n", p.Label, p.Path)

		if fi, err := os.Stat(p.Path); err != nil || !fi.IsDir() {
			ckFail.line("dir exists", "gone", "re-add or fix the path")
			continue
		}
		if _, ok := runCmd(p.Path, "", "git", "rev-parse", "--is-inside-work-tree"); !ok {
			ckFail.line("git repo", "not a git work tree", "the project dir must be a git repo")
			continue
		}

		if email, ok := runCmd(p.Path, "", "git", "config", "user.email"); ok && email != "" {
			ckPass.line("git identity", email, "")
		} else {
			ckFail.line("git identity", "user.email unset — commits would be misattributed",
				"git -C "+p.Path+" config user.email you@example.com (and user.name)")
		}

		if remote, ok := runCmd(p.Path, "", "git", "remote", "get-url", "origin"); !ok || remote == "" {
			ckWarn.line("git remote", "no 'origin' — pr/auto can't push", "add a remote, or use merge_policy manual")
		} else if _, ok := runCmd(p.Path, "", "git", "ls-remote", "--heads", "origin"); ok {
			ckPass.line("git push/remote reachable", "origin readable", "")
			// PR access: can gh see this repo with the resolved token?
			if _, ok := runCmd(p.Path, tokenEnv(tok), "gh", "repo", "view", "--json", "nameWithOwner"); ok {
				ckPass.line("GitHub PR access", "gh can resolve the repo", "")
			} else {
				ckFail.line("GitHub PR access", "gh can't resolve this repo from the daemon env",
					"set a token that can see it: `gh auth token | ptln daemon set-github-token`")
			}
		} else {
			ckFail.line("git remote reachable", "can't read origin (auth or network)",
				"check your SSH key / credentials for this remote")
		}

		eLabel := engineLabel(p.Engine)
		spec, known := eng.Lookup(eLabel)
		if !known {
			ckWarn.line("engine", eLabel+" unknown to this build", "check the project's engine setting")
		} else if _, err := exec.LookPath(spec.Bin); err == nil {
			ckPass.line("engine on PATH", eLabel+" ("+spec.Bin+")", "")
		} else {
			ckFail.line("engine on PATH", eLabel+" not found", "install the "+eLabel+" CLI ("+spec.Bin+")")
		}
	}

	fmt.Println("\nDone. Fix any ✗ before enqueuing a run; ⚠ are optional depending on merge policy.")
}

// tokenEnv returns a GH_TOKEN=… env pair for runCmd, or "" when there's no token (gh uses its own).
func tokenEnv(tok string) string {
	if tok == "" {
		return ""
	}
	return "GH_TOKEN=" + tok
}
