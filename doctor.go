package main

// doctor.go — `ptln doctor`: one command that answers "am I set up, and if not, exactly what is
// wrong and what do I type."
//
// WHY THIS IS THE FLOOR, NOT A FEATURE. partyline's machinery is largely correct; what it lacked was
// any way to SEE the state you are in — and several surfaces confidently reported a state you were
// not in. One afternoon produced all of these:
//
//	a deploy that said success and shipped nothing · a board card reading Building with nothing
//	running · a card reading Shipped with its PR still open · a chain labelled "stacked PRs, merge
//	in order" that was neither · `thread not found` for a thread that existed and was simply
//	unreadable · `project setup` reporting "using this repo's existing context thread" without ever
//	checking it · four filed work items that rendered on no page
//
// That is one bug in eight costumes: REPORTING A STATE NOBODY VERIFIED. Every line of the report
// below is a check that was actually performed, and every failure carries the command that fixes it.
// Where something cannot be determined, it says so rather than guessing — an "unknown" that is
// honest beats a "✓" that is not.
//
// Read-only and best-effort: doctor never changes anything, and a check that errors is reported as a
// failed check rather than taking the whole command down.

import (
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// doctorMain runs the setup report. Exit code is 1 when any REQUIRED check failed, so a script (or
// an agent) can branch on it without parsing text.
func doctorMain(args []string) {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Println("ptln doctor — is this machine and this repo set up to plan and run work?")
			fmt.Println()
			fmt.Println("  Checks your login, your daemon, and THIS repo's project + context thread,")
			fmt.Println("  and prints the exact command to fix anything that is wrong.")
			fmt.Println()
			fmt.Println("  Exit code 1 if anything required is broken, so scripts and agents can branch on it.")
			fmt.Println("  Read-only — it never changes anything.")
			fmt.Println()
			fmt.Println("  See also: ptln daemon doctor (crank prerequisites on this machine)")
			return
		}
	}

	fmt.Print("ptln doctor — can this repo plan and run work?\n\n")
	failed := 0
	report := func(s checkStatus, what, detail, fix string) {
		s.line(what, detail, fix)
		if s == ckFail {
			failed++
		}
	}

	// ── 1. identity ──────────────────────────────────────────────────────────────────────────────
	// Everything downstream needs a token, so a failure here explains every later one. Reported
	// first for that reason, and later checks are SKIPPED rather than guessed at when it fails.
	tok := api.LoadToken()
	if tok == "" {
		// Name the URL form, not bare `ptln login`. There is no hosted partyline to log in to, so
		// the bare command now refuses — sending someone to a command that refuses is worse than
		// not answering at all.
		fix := "ptln login https://your-instance.example.com   (or `ptln server install` to run one)"
		if !api.Unconfigured() {
			fix = "ptln login " + api.Base()
		}
		report(ckFail, "logged in", "no token on this machine", fix)
		fmt.Printf("\n%d required check(s) failed — nothing else can be checked until you log in.\n", failed)
		os.Exit(1)
	}
	c := api.New()
	me, merr := c.Me()
	switch {
	case merr != nil:
		report(ckFail, "logged in", "token present but the control plane rejected it: "+merr.Error(), "ptln login")
	case me == nil:
		report(ckFail, "logged in", "the control plane returned no profile", "ptln login")
	default:
		report(ckPass, "logged in", whoLabel(me), "")
	}

	// ── 2. this machine ──────────────────────────────────────────────────────────────────────────
	// A daemon is not required to PLAN, only to RUN — so this is a warning, not a failure. Saying
	// "you can plan but not start" is more useful than a red line that stops someone who only
	// wanted to file a task.
	if dev := loadDaemonDevice(); dev.DaemonID == "" {
		report(ckWarn, "this machine", "no daemon enrolled — you can plan, but not run work here",
			"ptln daemon enable")
	} else {
		report(ckPass, "this machine", "daemon enrolled ("+shortID(dev.DaemonID)+")", "")
	}

	// ── 3. the repo ──────────────────────────────────────────────────────────────────────────────
	cwd, _ := os.Getwd()
	root, rerr := gitwt.RepoRoot(cwd)
	if rerr != nil {
		report(ckWarn, "this directory", "not a git repository — partyline identifies a project by its git remote",
			"cd into a repo, or `git init` and add an origin")
		summarize(failed)
		return
	}
	remote := gitOriginURL(root)
	if remote == "" {
		report(ckFail, "this repo", "no `origin` remote — a local path is not an identity (it means a different repo on another machine)",
			"git remote add origin <url>")
		summarize(failed)
		return
	}
	report(ckPass, "this repo", doctorPath(root, 48), "")

	// ── 4. the context thread ────────────────────────────────────────────────────────────────────
	// THE CHECK THIS COMMAND EXISTS FOR. A pinned thread has three failure modes that used to be
	// indistinguishable, all reported as "thread not found":
	//   absent   — the pin names a thread that was deleted, or an id that was never a thread at all
	//   private  — the thread EXISTS and is simply not readable by you
	//   missing  — no pin, so planning has nowhere to file
	// Conflating "gone" with "not yours" cost an hour and a wrong commit: the session concluded the
	// thread was deleted, made a replacement, and repointed the repo away from its own history.
	pin := loadRepoBind(root)
	switch {
	case pin == "":
		report(ckFail, "context thread", "this repo has no thread pinned (.partyline.json)",
			"ptln project setup   — creates the project + thread and pins it, in one step")
	default:
		th, _, terr := c.GetThread(pin)
		switch {
		case terr == nil && th != nil:
			report(ckPass, "context thread", fmt.Sprintf("%s (%s)", th.Title, shortID(pin)), "")
		case terr != nil && isNotFound(terr):
			// Cannot tell absent from unreadable from here — the API answers 404 for both, by
			// design (a readable "it exists but is not yours" would leak the existence of other
			// teams' threads). So say BOTH, rather than picking one and being wrong half the time.
			report(ckFail, "context thread",
				fmt.Sprintf("thread %s (pinned in .partyline.json) is not readable — it was deleted, or it exists and is private to someone else", shortID(pin)),
				"ptln thread share "+pin+"   (ask whoever created it to run this)\n      → or: ptln project setup   to bind this repo to a thread you can read")
		default:
			report(ckFail, "context thread", "could not check thread "+shortID(pin)+": "+terr.Error(),
				"retry, or check your network")
		}
	}

	// ── 4b. local AI sessions, and where they are looked for ─────────────────────────────────────
	// Reported because "my session is missing" has an invisible cause: a session started under a
	// different $HOME is not missing, it is unsearched — and nothing on screen said so. The counts
	// are the point; a configured root that finds nothing reads identically to no root at all.
	sessions := collectSessions()
	extra := adoptedRootsLine()
	switch {
	case len(sessions) == 0 && extra == "":
		report(ckWarn, "local sessions", "none found under this account's home",
			"ptln llms --look-in <dir>   — if a session ran under a different HOME (a service "+
				"account, a second login), name that HOME; `ptln llms --roots` shows where it looks now")
	case len(sessions) == 0:
		report(ckWarn, "local sessions", "none found, including in "+extra,
			"ptln llms --roots   — check the adopted paths are the HOMEs the sessions actually ran under")
	case extra != "":
		report(ckPass, "local sessions", fmt.Sprintf("%d %s (also looking in %s)", len(sessions), plural(len(sessions), "session", "sessions"), extra), "")
	default:
		report(ckPass, "local sessions", fmt.Sprintf("%d %s", len(sessions), plural(len(sessions), "session", "sessions")), "")
	}

	// ── 5. the project, and whether THIS machine can build it ────────────────────────────────────
	// Two separate things people conflate: a project EXISTS in the team (so work can be planned
	// against it), and this machine ADVERTISES it (so work can actually run here). Reporting them
	// as one line is how "promote refused" becomes mysterious.
	label, _, _, lerr := c.ResolveRepo(remote)
	switch {
	case lerr != nil:
		report(ckWarn, "project", "could not ask the control plane: "+lerr.Error(), "retry")
	case label == "":
		report(ckFail, "project", "no project in your team points at this repo",
			"ptln project setup   — creates it and registers this directory, in one step")
	default:
		report(ckPass, "project", label, "")

		// DO THE TWO AGREE? The repo pins a thread in .partyline.json and the project resolves to a
		// thread of its own, and NOTHING checked they are the same one. Reporting each as ✓
		// independently is how a repo ends up with a split brain that reads as healthy: this very
		// check was added because cyberpunk-game pinned a thread holding all 15 of its facts while
		// its project resolved to a different, EMPTY thread. Sessions following the pin saw the
		// history; anything resolving through the project saw nothing; both lines said ✓.
		if pin != "" {
			if pth, perr := c.ResolveThreadForProject(label); perr == nil && pth != nil && pth.ID != pin {
				report(ckFail, "thread agreement",
					fmt.Sprintf("this repo pins %s but project %q resolves to %s — two different threads, so what you "+
						"record depends on which door you came in by", shortID(pin), label, shortID(pth.ID)),
					"ptln thread use "+pin+" <project-id>   — make the project resolve to the thread this repo uses\n"+
						"      (`ptln project show <id>` names both; keep the one with the history.\n"+
						"      NOT `thread attach` — that governs canon inheritance, not which thread resolves)")
			} else if perr == nil && pth != nil {
				report(ckPass, "thread agreement", "the repo and the project resolve to the same thread", "")
			}
		}

		if projectByLabel(loadDaemonRegistry(), label) == nil {
			report(ckWarn, "runnable here", "this machine does not advertise "+label+" — you can plan, but work will not start on this box",
				"ptln daemon add-project "+label+" "+root)
		} else {
			report(ckPass, "runnable here", "registered on this machine", "")
		}
	}

	summarize(failed)
}

func summarize(failed int) {
	fmt.Println()
	if failed == 0 {
		fmt.Println("Everything required is in place.")
		return
	}
	fmt.Printf("%d required check(s) failed — fix the → lines above, then run `ptln doctor` again.\n", failed)
	os.Exit(1)
}

// isNotFound reports whether an API error is the control plane saying "no such thing, or not yours".
// Deliberately shallow: doctor only needs to distinguish "the answer was no" from "the call broke",
// because a network error and a missing thread need completely different advice.
func isNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "404")
}

func whoLabel(p *api.Profile) string {
	if p == nil {
		return ""
	}
	for _, s := range []string{p.Handle, p.DisplayName} {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return "signed in"
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func doctorPath(s string, n int) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(s, home) {
		s = "~" + s[len(home):]
	}
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-(n-1):]
}
