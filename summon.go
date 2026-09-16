package main

// EXPERIMENTAL — prototype on branch proto/summon-agents. NOT wired into the
// product surface (help/docs) on purpose.
//
// `ptln summon` spawns an AI agent into a watchable, end-to-end-encrypted
// partyline session — either RESUMED from your local llms inventory or started
// FRESH — optionally seeded with an initial task. It is the de-risking spike for
// the larger idea: "start a party → wake LLMs into terminals everyone can watch
// the full output of."
//
// Design choices that keep this a clean, low-risk experiment:
//   - It reuses the EXISTING host path: it composes the equivalent
//     `ptln -- <agent argv>` invocation and execs it, so all the relay /
//     join-link / E2EE machinery is the production code — untouched.
//   - The task is seeded via the agent's ARGV (e.g. `claude "<task>"`), not by
//     injecting into stdin. That sidesteps the prompt-injection timing/security
//     problem entirely for the prototype.
//
// What this proves: resolve-from-inventory → spawn-into-watchable-session →
// seed-a-task, end to end, 100% local (no backend trigger, no daemon, no remote
// authz — those are the productization, deliberately out of scope here).

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func summonMain(args []string) {
	fs := flag.NewFlagSet("summon", flag.ExitOnError)
	fresh := fs.Bool("fresh", false, "start a fresh agent instead of resuming a session")
	tool := fs.String("tool", "claude", "agent to start with --fresh (claude|codex|gemini)")
	task := fs.String("task", "", "initial task to seed the agent with (passed as its first argument)")
	_ = fs.Parse(args)
	rest := fs.Args()

	var argv []string
	var dir string

	if *fresh {
		argv = []string{*tool}
	} else {
		if len(rest) == 0 {
			fatal(fmt.Errorf("usage:\n  ptln summon <session-id> [--task \"…\"]      resume one of your AI sessions, watchable\n  ptln summon --fresh [--tool claude] [--task \"…\"]   start a fresh agent\nRun `ptln llms` to see resumable session ids."))
		}
		idArg := rest[0]
		var match *aiSession
		for i := range collectSessionsOnce() {
			s := &summonSessions[i]
			if s.resumeArgv == nil {
				continue
			}
			if s.ID == idArg || strings.HasPrefix(s.ID, idArg) {
				match = s
				break
			}
		}
		if match == nil {
			fatal(fmt.Errorf("no resumable session matches %q — run `ptln llms` to see ids", idArg))
		}
		argv = append([]string{}, match.resumeArgv...)
		dir = match.resumeDir
	}

	// Seed the task via argv (claude accepts a positional initial prompt). Other
	// tools vary; the prototype targets claude.
	if *task != "" {
		argv = append(argv, *task)
	}

	// chdir to the session's recorded project dir so the resumed agent has context.
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "ptln summon: can't cd to %s (%v) — continuing in current dir\n", dir, err)
		}
	}

	// Hand off to the production host path: `ptln -- <agent argv>`. exec replaces
	// this process (inheriting the terminal), exactly like `ptln llms` resume —
	// so the agent runs inside a shared, E2EE session with a printed join link.
	self, err := os.Executable()
	if err != nil {
		fatal(fmt.Errorf("summon: %w", err))
	}
	full := append([]string{self, "--"}, argv...)
	fmt.Printf("☎ summoning into a watchable session: %s\n", strings.Join(argv, " "))
	if err := syscall.Exec(self, full, os.Environ()); err != nil {
		fatal(fmt.Errorf("summon exec failed: %w", err))
	}
}

// summonSessions caches the inventory so the resolve loop can take addresses.
var summonSessions []aiSession

func collectSessionsOnce() []aiSession {
	if summonSessions == nil {
		summonSessions = collectSessions()
	}
	return summonSessions
}
