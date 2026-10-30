package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Checks run in the repository itself, not in a copy: a copy has no node_modules, virtualenv or
// build cache, so most projects' tests cannot run there. They run through the person's login
// shell, and through direnv when the repository has an .envrc, so they see the tools and versions
// the person does. The tree is measured before and after; a check run during which the work
// changed is marked stale rather than trusted.

const loopCheckTimeout = 10 * time.Minute

type loopCheck struct {
	Cmd     string `json:"cmd"`
	Exit    int    `json:"exit"` // -1: did not finish (timeout or could not start)
	Seconds int    `json:"seconds"`
	Tail    string `json:"tail,omitempty"` // last lines of output, for the session
}

type loopCheckRound struct {
	Checks []loopCheck `json:"checks"`
	Tree   string      `json:"tree"`  // working tree the checks ran against
	Stale  bool        `json:"stale"` // the tree changed while they ran
}

// loopCheckCommand is the argv that runs cmd in root the way the person's own terminal would.
func loopCheckCommand(root, cmd string) []string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	argv := []string{shell, "-lc", cmd}
	if _, err := os.Stat(filepath.Join(root, ".envrc")); err == nil {
		if direnv, err := exec.LookPath("direnv"); err == nil {
			argv = append([]string{direnv, "exec", root}, argv...)
		}
	}
	return argv
}

// runLoopChecks runs every command in root, appending full output to logPath, with the person's
// environment (checksEnv).
func runLoopChecks(root string, cmds []string, logPath string) loopCheckRound {
	return runLoopChecksEnv(root, cmds, logPath, checksEnv(false))
}

// runLoopChecksFor runs r's checks: a builder-driven step's run without the person's credentials,
// since they run code a builder wrote outside the builder's sandbox.
func runLoopChecksFor(r *loopRun, cmds []string, logPath string) loopCheckRound {
	return runLoopChecksEnv(r.Repo, cmds, logPath, checksEnv(builderDriven(r)))
}

// checksEnv keeps the person's environment (tokens included: their tests may need them) but not the
// identifiers that let a process act as the session. scrubbed also drops credentials and the
// channels to other systems, as for reviewers (secretEnv).
func checksEnv(scrubbed bool) []string {
	env := withoutEnv(os.Environ(), "PARTYLINE_SESSION_KEY", "TMUX", "TMUX_PANE")
	if !scrubbed {
		return env
	}
	out := env[:0:0]
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !secretEnv.MatchString(k) {
			out = append(out, kv)
		}
	}
	return out
}

func runLoopChecksEnv(root string, cmds []string, logPath string, env []string) loopCheckRound {
	var out loopCheckRound
	before, _ := workTreeID(root)
	log, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if log != nil {
		defer log.Close()
	}
	for _, c := range cmds {
		out.Checks = append(out.Checks, runLoopCheck(root, c, log, env))
	}
	after, _ := workTreeID(root)
	out.Tree, out.Stale = after, before == "" || before != after
	return out
}

func runLoopCheck(root, cmd string, log *os.File, env []string) loopCheck {
	ctx, cancel := context.WithTimeout(context.Background(), loopCheckTimeout)
	defer cancel()
	argv := loopCheckCommand(root, cmd)
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = root
	c.Env = env
	groupSpawn(c)
	start := time.Now()
	output, err := c.CombinedOutput()
	res := loopCheck{Cmd: cmd, Seconds: int(time.Since(start).Seconds()), Tail: tailText(string(output), 30)}
	switch {
	case ctx.Err() != nil:
		res.Exit, res.Tail = -1, fmt.Sprintf("timed out after %s\n%s", loopCheckTimeout, res.Tail)
	case err == nil:
		res.Exit = 0
	default:
		res.Exit = -1
		if ee, ok := err.(*exec.ExitError); ok {
			res.Exit = ee.ExitCode()
		}
	}
	if log != nil {
		fmt.Fprintf(log, "\n$ %s   (exit %d, %ds)\n%s", cmd, res.Exit, res.Seconds, output)
	}
	return res
}

// workTreeID is the tree id of root's working tree now, without exporting it. Two equal ids mean
// nothing under review changed.
func workTreeID(root string) (string, error) {
	tmp, err := os.MkdirTemp("", "ptln-tree-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	return writeWorkTree(root, filepath.Join(tmp, "index"))
}

// newFailures are checks that fail now and did not fail at baseline. A check failing on the base
// commit already is the repository's problem, not the run's.
func newFailures(now, baseline []loopCheck) []loopCheck {
	failedBefore := map[string]bool{}
	for _, c := range baseline {
		if c.Exit != 0 {
			failedBefore[c.Cmd] = true
		}
	}
	var out []loopCheck
	for _, c := range now {
		if c.Exit != 0 && !failedBefore[c.Cmd] {
			out = append(out, c)
		}
	}
	return out
}

func withoutEnv(env []string, drop ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, d := range drop {
			if k == d {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

func tailText(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
