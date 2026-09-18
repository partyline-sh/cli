// scribe_cmd.go — `ptln scribe`: run ONE Mode-4 capture pass on demand.
//
// This is the manual trigger for the distiller in scribe.go. It exists first (before the automatic
// cadence) as the end-to-end harness: run it inside a real session, read the facts it lands on the
// thread, and judge the distillation quality before wiring it to fire automatically. The automatic
// cadence (mux goroutine, run lifecycle) later calls the SAME runScribePass — this command is the
// human-in-the-loop way to verify the piece that has no clean automated grade.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"syscall"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptymux"
)

func scribeMain(args []string) {
	fs := flag.NewFlagSet("scribe", flag.ExitOnError)
	threadFlag := fs.String("thread", "", "thread id to capture into (default: this session's / the cwd project's thread)")
	sessionFlag := fs.String("session", "", "engine session id to distill (default: the newest session in this directory)")
	minTurns := fs.Int("min-turns", 1, "skip the pass if fewer than this many new turns since the last (cost guard)")
	_ = fs.Parse(args)

	client := api.New()
	if client.Token == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login` first"))
	}

	thread := resolveScribeThread(client, *threadFlag)
	if thread == "" {
		fatal(fmt.Errorf("no thread to capture into — attach one with `ptln new <tool> --thread <id>`, run inside a project, or pass --thread"))
	}

	sess, ok := resolveCwdSession(*sessionFlag)
	if !ok {
		fatal(fmt.Errorf("no AI session found in this directory — run this from where your claude/codex session is working, or pass --session"))
	}

	fmt.Printf("distilling %s session %s → thread %s\n", sess.Tool, short(sess.ID, 8), short(thread, 8))
	written, err := runScribePass(client, thread, sess.Tool, sess.Tool, "", sess.ID, sess.storePath, *minTurns, func(s string) { fmt.Println("  " + s) })
	if err != nil {
		fatal(fmt.Errorf("scribe: %w", err))
	}
	switch {
	case written == 0:
		fmt.Println("no new durable facts to record (nothing new past the watermark, or nothing worth capturing).")
	case written == 1:
		fmt.Println("recorded 1 fact to the thread.")
	default:
		fmt.Printf("recorded %d facts to the thread.\n", written)
	}
}

// scribeOnQuit is the exit cadence: when the mux quits, capture each attached session before its
// context goes cold. It must NOT block the quit — a distill pass spawns an engine and takes a minute —
// so it fires a DETACHED `ptln scribe` subprocess per attached session and returns immediately. Each
// child does its own watermark-forward pass after the mux is gone; the min-turns guard makes a quit
// with nothing new essentially free. Best-effort throughout: no binary, no thread, a spawn that fails —
// the quit proceeds regardless. Only sessions with a thread attached are captured (nothing to write to
// otherwise), mirroring the manual command's gate.
func scribeOnQuit(specs []ptymux.Spec) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	for _, s := range specs {
		if s.Thread == "" || s.Key == "" {
			continue
		}
		cmd := exec.Command(self, "scribe", "--thread", s.Thread, "--session", s.Key)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group → survives the mux's exit / terminal teardown
		if err := cmd.Start(); err != nil {
			continue
		}
		_ = cmd.Process.Release() // detach: the pass outlives this process
	}
}

// resolveScribeThread: the flag, else this session's attached thread (PARTYLINE_THREAD_ID), else the
// cwd project's thread (server-resolved, lazily created), else "".
func resolveScribeThread(client *api.Client, flagID string) string {
	if flagID != "" {
		return flagID
	}
	if env := os.Getenv("PARTYLINE_THREAD_ID"); env != "" {
		return env
	}
	if label := projectLabelForCwd(); label != "" {
		if t, err := client.ResolveThread(label); err == nil && t != nil {
			return t.ID
		}
	}
	return ""
}

// resolveCwdSession returns the session to distill: the one matching --session if given, else the
// most-recently-active claude/codex session whose cwd is the current directory.
func resolveCwdSession(sessionID string) (aiSession, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return aiSession{}, false
	}
	sessions := append(claudeSessions(home), codexSessions(home)...)
	if sessionID != "" {
		for _, s := range sessions {
			if s.ID == sessionID {
				return s, true
			}
		}
		return aiSession{}, false
	}
	cwd, err := os.Getwd()
	if err != nil {
		return aiSession{}, false
	}
	var here []aiSession
	for _, s := range sessions {
		if s.Cwd == cwd && s.storePath != "" {
			here = append(here, s)
		}
	}
	if len(here) == 0 {
		return aiSession{}, false
	}
	sort.Slice(here, func(i, j int) bool { return here[i].LastActive.After(here[j].LastActive) })
	return here[0], true
}
