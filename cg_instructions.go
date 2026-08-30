package main

import (
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// cg_instructions.go — what the model is told about partyline the moment the session opens.
//
// This is the `instructions` field of the MCP initialize result: the host injects it into the system
// prompt, so unlike a tool description or an MCP prompt it is present from turn one, is never
// something the model has to decide to go and read, and updates with the binary rather than with a
// file someone has to remember to paste into their repo.
//
// It exists because of a specific, repeated failure. An LLM session in a repo that was not set up
// correctly did not report "this repo is not set up" — it reported a THREAD NOT FOUND error, guessed
// at causes, tried tools in the wrong order, and eventually told the user their account was wrong
// when it was not. The model had no way to know what a correctly-set-up repo even looks like, so it
// could not tell "misconfigured" from "broken", and neither could the user reading its output.
//
// So the briefing is STATE-AWARE. It does not describe partyline in the abstract; it says where THIS
// repo actually stands and what the single next action is. A model that is told "no thread is pinned
// here — run `ptln project setup`" cannot spend twenty turns discovering that.
//
// Kept strictly LOCAL and synchronous: files and env only, no network. initialize blocks the start of
// the session, and a briefing that makes the editor hang on a slow control plane would be a worse bug
// than the one it fixes. Anything needing the server is delegated to `ptln doctor`, which is named
// here precisely so the model asks the machine instead of guessing.
func cgInstructions(thread string) string {
	var b strings.Builder
	b.WriteString(`partyline — shared context and autonomous builds for this repo.

WHAT IT IS. A context thread is the team's durable memory for a project: decisions, constraints and
interface contracts that outlive any one session. You read it with recall and add to it with
remember. A work item is a planned piece of work; promoting one dispatches a "crank" worker that
builds it in a git worktree on a real machine and opens a PR.

`)

	// ── where this session actually stands ────────────────────────────────────────────────────────
	b.WriteString("THIS SESSION.\n")
	switch {
	case api.LoadToken() == "":
		b.WriteString("  NOT SIGNED IN on this machine. Nothing below will work. Tell the user to run `ptln login`\n" +
			"  in a terminal — you cannot do it for them, it needs a browser. Do not retry the tools meanwhile.\n")
	case thread == "":
		b.WriteString("  NO CONTEXT THREAD is bound to this session, so recall/remember have nowhere to go.\n")
		if root, err := gitwt.RepoRoot(cwdOrEmpty()); err == nil {
			if gitOriginURL(root) == "" {
				b.WriteString("  This repo has no `origin` remote. partyline identifies a project by its remote, because a\n" +
					"  local path means a different repo on someone else's machine. Ask the user to add one.\n")
			} else {
				b.WriteString("  Fix it with the create_project tool (or tell the user `ptln project setup`). That ONE step\n" +
					"  creates the project, gives it a shared thread, pins it in .partyline.json and registers this\n" +
					"  directory. Do not try to assemble those pieces yourself with separate calls.\n")
			}
		} else {
			b.WriteString("  This directory is not a git repository, so there is nothing stable to key a project on.\n")
		}
	default:
		b.WriteString("  Context thread " + shortID(thread) + " is bound. recall/remember work; read before you rely on it.\n")
	}

	b.WriteString(`
IF A TOOL FAILS, DO NOT GUESS. Run ` + "`ptln doctor`" + ` (read-only, safe, no arguments) and report what it
says. It checks sign-in, this machine, this repo, the thread and the project, and every failing line
carries the exact command that fixes it. A "not found" from these tools almost always means this repo
is not set up — NOT that the user's account is wrong. Never tell a user their account or identity is
at fault on the strength of a not-found error; that has sent people on long hunts for nothing.

PLANNING. To turn an idea into work partyline can build, use planning_open, then planning_note, then
planning_finalize. Do not hand-assemble work items: finalize applies the same specificity gate the
board applies at start time, so anything it accepts is guaranteed to be startable. It refuses until
the work names a target, carries acceptance criteria including one EXECUTABLE check, and has no
unanswered open questions. Those refusals are the point — record what you need from the human as an
open question rather than deciding it yourself and moving on.

FILING IS NOT STARTING. Finalizing files a plan; nothing runs until it is promoted. Say so plainly
rather than implying work has begun.
`)
	return b.String()
}

// cwdOrEmpty is os.Getwd without the error branch, for the briefing's benefit: a working directory we
// cannot read is simply "no repo here", which is already a case the text handles.
func cwdOrEmpty() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}
