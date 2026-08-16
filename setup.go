package main

import (
	"fmt"
	"os"
	"runtime"
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
// Trigger policy (#149, deliberate): `ptln login` IS the setup command — the checklist runs
// after EVERY successful login, not just the first. A configured step renders ✓ and asks
// nothing, so on a healthy machine the whole pass is a glance; a gap asks its one question;
// the code-locations prompt keeps the current answer on Enter. Logging in is the explicit
// "I want the connected experience" signal, and the daemon can't enrol without an account,
// so login is genuinely the front door to everything this configures. Logged-out/local-only
// users never see setup; everyone else can also run `ptln setup` by hand any time.

func setupInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func askYes(prompt string) bool {
	fmt.Printf("%s [Y/n] ", prompt)
	a := strings.ToLower(strings.TrimSpace(readLine()))
	return a == "" || strings.HasPrefix(a, "y")
}

// inSetup suppresses the login→setup re-entry while setup ITSELF drives a login (the redo
// account switch) — without it, switching accounts mid-checklist would nest a second checklist.
var inSetup bool

func setupMain(args []string) {
	redo := false
	for _, a := range args {
		if a == "--redo" || a == "redo" || a == "--fresh" {
			redo = true
		}
	}
	runSetup(redo)
}

func runSetup(redo bool) {
	inSetup = true
	defer func() { inSetup = false }()
	if redo {
		fmt.Print("ptln setup — rebuilding this machine's settings (Enter keeps the current answer)\n\n")
	} else {
		fmt.Print("ptln setup — connecting this machine\n\n")
	}
	interactive := setupInteractive()
	redo = redo && interactive // a re-ask nobody can answer is a hang

	// ── 0 · CLI version — an outdated binary is missing setup steps entirely ─
	// The failure this line exists for: a machine on an old release runs `ptln login`, gets the
	// old behavior, and nothing on screen says why. Bounded synchronous check, best-effort.
	if !updateChecksDisabled() {
		if latest, _, _, err := api.New().LatestVersion(version, runtime.GOOS); err == nil && latest != "" && versionLess(version, latest) {
			ckWarn.line("cli", version+" — "+latest+" is available", "ptln update")
		} else {
			ckPass.line("cli", version, "")
		}
	}

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
		if redo && !askYes("  1. Account — logged in as "+who+". Keep this account?") {
			loginMain()
			fmt.Println()
		} else {
			ckPass.line("account", who, "")
		}
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
		if redo && !askYes("  2. Worker — enrolled (device "+dev.DaemonID+"), always-on "+state+". Keep this machine as a worker?") {
			// Changing your mind is the whole point of redo: tear it down cleanly — service
			// out, device token revoked — the mirror of what the worker question builds.
			_ = uninstallService()
			daemonDisable()
			ckWarn.line("worker", "disabled — this machine no longer receives work", "re-run `ptln setup` to re-enable")
		} else {
			ckPass.line("worker", "enrolled (device "+dev.DaemonID+"), always-on "+state, "")
		}
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
	setupCodeLocations(interactive, redo)

	// ── 5 · Projects — a node with nothing bound receives no work ───────────
	reg := loadDaemonRegistry()
	if redo && len(reg.Projects) > 0 {
		kept := reg.Projects[:0]
		changed := false
		for _, p := range reg.Projects {
			if askYes(fmt.Sprintf("     Keep project %q (%s)?", p.Label, p.Path)) {
				kept = append(kept, p)
			} else {
				changed = true
				fmt.Printf("     ✓ removed %q — the board can no longer dispatch to it here\n", p.Label)
			}
		}
		if changed {
			reg.Projects = kept
			if err := saveDaemonRegistry(reg); err == nil {
				syncIfEnrolled()
			}
			reg = loadDaemonRegistry()
		}
	}
	if len(reg.Projects) == 0 {
		ckWarn.line("projects", "none bound — this machine can't receive work yet",
			"pick a repo at "+api.Base()+"/settings/integrations (this machine advertises its git repos automatically), or `ptln daemon add-project <label> [dir]`")
	} else {
		// Paths, not just labels. "2 bound (tire-shop-intel, partyline)" once looked perfectly
		// healthy while one entry pointed at the user's ENTIRE HOME DIRECTORY and the other at a
		// directory that no longer existed — the label list hid both. The map from label to path
		// is this machine's whole capability grant; setup must show the grant, not its names.
		home, _ := os.UserHomeDir()
		bad := 0
		for _, p := range reg.Projects {
			switch {
			case home != "" && p.Path == home:
				bad++
				ckWarn.line("project "+p.Label, p.Path+" — YOUR ENTIRE HOME DIRECTORY as the working tree",
					"re-point it: `ptln daemon add-project "+p.Label+" <repo dir>` (or remove-project)")
			case !dirExistsSetup(p.Path):
				bad++
				ckWarn.line("project "+p.Label, p.Path+" — directory no longer exists",
					"`ptln daemon remove-project "+p.Label+"` (or re-create the directory)")
			default:
				ckPass.line("project "+p.Label, p.Path, "")
			}
		}
		_ = bad
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

// dirExistsSetup reports whether a registry path still points at a real directory.
func dirExistsSetup(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// setupCodeLocations reports what the repo scan finds and asks where else code lives. The found
// count comes FIRST so the person can judge the question — "38 found" usually means Enter, "0
// found" means the machine's code is somewhere the scan doesn't reach. Loops so several mounts
// can be added; a bad path reports and re-asks rather than aborting setup.
func setupCodeLocations(interactive, redo bool) {
	removed := 0
	if redo {
		reg := loadDaemonRegistry()
		kept := reg.ScanRoots[:0]
		for _, r := range reg.ScanRoots {
			if askYes("     Keep scanning " + r + "?") {
				kept = append(kept, r)
			} else {
				removed++
				fmt.Printf("     ✓ no longer scanning %s (already-bound projects there are untouched)\n", r)
			}
		}
		if removed > 0 {
			reg.ScanRoots = kept
			if err := saveDaemonRegistry(reg); err == nil {
				invalidateLocalRepoCache()
			}
		}
	}
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
	// The running service caches its scan for 10 minutes — bounce it so root changes reach the
	// picker on the next heartbeat, not most of the way through a coffee.
	if (added > 0 || removed > 0) && serviceInstalled() && serviceActive() {
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

// runSetupAfterLogin runs at the end of every successful `ptln login`. Not an offer, not
// once-only: login IS setup. Cheap on a configured machine — each done step is one ✓ line —
// and Enter through the code-locations question keeps everything as it was. (The old
// once-only offer file, ~/.partyline/setup-offer.json, is simply no longer consulted.)
func runSetupAfterLogin() {
	if inSetup || !setupInteractive() {
		return
	}
	fmt.Println()
	runSetup(false)
}
