package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/api"
)

// setup.go — `ptln setup`: one command from "binary installed" to "this machine builds work".
//
// Seven checks in dependency order — account, worker, engine, code locations, projects, pull
// requests, agent memory. A finished step shows ✓ and is never redone; a gap either asks ONE question (on a
// TTY) or names the exact fix (piped/CI — same read-only posture as doctor). Idempotent, so
// every error breadcrumb and status line can point here without fear of it re-doing anything.
//
// Trigger policy (#149, deliberate): the ACTIVE offer happens exactly once, right after a
// successful `ptln login` — logging in is the explicit "I want the connected experience"
// signal, and it's the only prompt moment. Logged-out/local-only users never see setup;
// the daemon can't enrol without an account, so login is genuinely the front door to
// everything this configures. Everyone else gets passive signposts (status lines, error
// breadcrumbs, this command by hand).

func setupInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func askYes(prompt string) bool {
	fmt.Printf("%s [Y/n] ", prompt)
	a := strings.ToLower(strings.TrimSpace(readLine()))
	return a == "" || strings.HasPrefix(a, "y")
}

func setupMain() {
	fmt.Print("ptln setup — connecting this machine\n\n")
	interactive := setupInteractive()

	// ── 1 · Account ─────────────────────────────────────────────────────────
	if api.LoadToken() == "" {
		if !interactive || !askYes("  1. Account — not logged in. Log in now?") {
			ckFail.line("account", "not logged in", "run `ptln login` — every later step needs it")
			return
		}
		loginMain() // exits the process on failure, which correctly ends setup too
		fmt.Println()
	} else {
		who := strings.TrimSpace(api.LoadAccount().Email)
		if who == "" {
			who = "logged in"
		}
		ckPass.line("account", who, "")
	}

	// ── 2 · Worker — enrolment + always-on service, as ONE question ─────────
	// `enable` and `install` being two commands you had to know about is exactly how a machine
	// ends up enrolled but running nothing (and invisible). Here it's one yes.
	dev := loadDaemonDevice()
	revoked := dev.Token != "" && deviceRevoked(dev)
	switch {
	case dev.Token != "" && !revoked && serviceInstalled():
		state := "installed"
		if serviceActive() {
			state = "running"
		}
		ckPass.line("worker", "enrolled (device "+dev.DaemonID+"), always-on "+state, "")
	case !interactive:
		ckFail.line("worker", "not set up as an always-on worker",
			"run `ptln setup` in a terminal, or `ptln daemon enable && ptln daemon install`")
	default:
		if revoked {
			fmt.Println("  ⚠ this machine's device token was revoked — it needs a fresh enrolment.")
		}
		if askYes("  2. Worker — run this machine as an always-on worker?\n     It starts on boot and receives work from your team.") {
			setupWorker(dev, revoked)
		} else {
			fmt.Println("     skipped — later: `ptln daemon enable && ptln daemon install`")
		}
	}

	// ── 3 · Engine — workers run agents with YOUR model login ───────────────
	engines := installedEngines()
	if len(engines) == 0 {
		ckWarn.line("engine", "no AI CLI found on PATH",
			"install one (Claude Code, Codex, Gemini…) — workers need it to build")
	} else {
		ckPass.line("engine", engineList(engines)+" found", "")
	}

	// ── 4 · Code locations — where this machine's repositories live ─────────
	// Home is scanned automatically, but plenty of machines keep code on a mount (/srv, a data
	// drive, a second disk) — on those, the repo picker is honestly empty and nothing hints why.
	// Ask HERE, where the person is already answering setup questions, not in a command they'd
	// have to discover. Enter skips; every answer becomes a scan root advertised on the heartbeat.
	setupCodeLocations(interactive)

	// ── 5 · Projects — a node with nothing bound receives no work ───────────
	reg := loadDaemonRegistry()
	if len(reg.Projects) == 0 {
		ckWarn.line("projects", "none bound — this machine can't receive work yet",
			"pick a repo at "+api.Base()+"/settings/integrations (this machine advertises its git repos automatically), or `ptln daemon add-project <label> [dir]`")
	} else {
		labels := make([]string, 0, len(reg.Projects))
		for _, p := range reg.Projects {
			labels = append(labels, p.Label)
		}
		ckPass.line("projects", fmt.Sprintf("%d bound (%s)", len(labels), strings.Join(labels, ", ")), "")
	}

	// ── 5 · Pull requests (optional) ────────────────────────────────────────
	if resolveGitHubToken() == "" {
		ckWarn.line("pull requests", "no GitHub token — agents can build branches but can't open PRs",
			"`gh auth login`, then `gh auth token | ptln daemon set-github-token`")
	} else {
		ckPass.line("pull requests", "GitHub token resolved", "")
	}

	// ── 6 · Agent memory — recall/remember wired per engine (#557/#558) ─────
	// Probed from the engines' real config files (mcpWirings), not the first-run offer state —
	// a manual `ptln thread connect` or a rotted registration must read true here.
	if wirings := mcpWirings(); len(wirings) > 0 {
		var wired, unwired, fixes []string
		for _, w := range wirings {
			if w.status == ckPass {
				wired = append(wired, w.name)
			} else {
				unwired = append(unwired, w.name)
				fixes = append(fixes, "`"+w.fix+"`")
			}
		}
		if len(unwired) == 0 {
			ckPass.line("agent memory", "MCP wired for "+strings.Join(wired, ", "), "")
		} else {
			ckWarn.line("agent memory", "not wired for "+strings.Join(unwired, ", "),
				strings.Join(fixes, " · "))
		}
	}

	fmt.Println()
	if d := loadDaemonDevice(); d.Token != "" && serviceInstalled() {
		fmt.Printf("Connected — this machine shows in your fleet at %s/dashboard.\n", api.Base())
	}
	fmt.Println("Re-run `ptln setup` any time; it only touches what's missing. Deeper checks: `ptln daemon doctor`.")
}

// setupCodeLocations reports what the repo scan finds and asks where else code lives. The found
// count comes FIRST so the person can judge the question — "38 found" usually means Enter, "0
// found" means the machine's code is somewhere the scan doesn't reach. Loops so several mounts
// can be added; a bad path reports and re-asks rather than aborting setup.
func setupCodeLocations(interactive bool) {
	found := len(scanLocalRepos())
	roots, _ := localRepoRoots()
	where := "your home directory"
	if len(roots) > 1 {
		where += " + " + strings.Join(roots[1:], ", ")
	}
	status := ckPass
	if found == 0 {
		status = ckWarn
	}
	status.line("code", fmt.Sprintf("%d git repositor%s found under %s", found, plural(found, "y", "ies"), where),
		"if this machine's code lives elsewhere: `ptln daemon scan-root add <dir>`")
	if !interactive {
		return
	}
	added := 0
	for {
		fmt.Print("     Code somewhere else too (a mount, a data drive)? path, or Enter to continue: ")
		p := strings.TrimSpace(readLine())
		if p == "" {
			break
		}
		abs, n, err := addScanRoot(p)
		if err != nil {
			fmt.Printf("     ✗ %v — try again or press Enter\n", err)
			continue
		}
		added++
		fmt.Printf("     ✓ scanning %s — %d repositor%s found there\n", abs, n, plural(n, "y", "ies"))
	}
	// The running service caches its scan for 10 minutes — bounce it so the new roots reach the
	// picker on the next heartbeat, not most of the way through a coffee.
	if added > 0 && serviceInstalled() && serviceActive() {
		if err := restartService(); err == nil {
			fmt.Println("     ✓ daemon restarted — advertising the new locations now")
		}
	}
}

// setupWorker makes the machine an always-on worker: enrol (fresh or replacing a revoked
// credential), install the service, and restart it if it was already running on a dead token.
func setupWorker(dev daemonDevice, revoked bool) {
	enrolled := false
	if dev.Token == "" || revoked {
		d, err := enrollDevice(defaultDeviceLabel())
		if err != nil {
			ckFail.line("worker", "enrolment failed: "+err.Error(), "fix and re-run `ptln setup`")
			return
		}
		fmt.Printf("     ✓ enrolled as %q (device %s)\n", defaultDeviceLabel(), d.DaemonID)
		enrolled = true
	}
	if !serviceInstalled() {
		note, err := installService()
		if err != nil {
			ckFail.line("worker", "service install failed: "+err.Error(), "try `ptln daemon install`")
			return
		}
		fmt.Println("     ✓ always-on: " + note)
	} else if enrolled {
		// The service exists but was running (or crash-looping) on the old credential.
		if err := restartService(); err == nil {
			fmt.Println("     ✓ service restarted on the new credential")
		}
	}
}

// deviceRevoked asks the control plane whether this device credential still works. Only a
// definite auth rejection counts — a network error must NOT read as revoked, or a flaky
// connection would nuke a healthy enrolment.
func deviceRevoked(d daemonDevice) bool {
	_, _, err := api.DaemonOwner(d.Base, d.Token)
	return err != nil && isAuthErr(err)
}

// ── the once-only post-login offer ──────────────────────────────────────────

func setupOfferPath() string { return filepath.Join(stateDir(), "setup-offer.json") }

// shouldOfferSetup is the offer rule, extracted so it's testable as a RULE: ask iff the
// terminal can answer, the machine isn't already a worker, and we've never asked before.
// (Asked-once regardless of answer — a "yes" that then skips the worker step inside setup
// was still an answer; re-asking on every login is how an offer becomes nagware.)
func shouldOfferSetup(interactive, connected, askedBefore bool) bool {
	return interactive && !connected && !askedBefore
}

// offerSetupAfterLogin runs at the end of a successful `ptln login` — the one active trigger.
func offerSetupAfterLogin() {
	connected := loadDaemonDevice().Token != "" && serviceInstalled()
	_, statErr := os.Stat(setupOfferPath())
	if !shouldOfferSetup(setupInteractive(), connected, statErr == nil) {
		return
	}
	fmt.Println()
	answer := askYes("This machine isn't connected as a worker yet — finish setup?")
	_ = os.WriteFile(setupOfferPath(), []byte("{\"asked\":true}\n"), 0o600)
	if !answer {
		fmt.Println(dimS("  Later: ptln setup"))
		return
	}
	fmt.Println()
	setupMain()
}
