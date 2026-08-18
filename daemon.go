package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sort"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
	"partyline.sh/partyline/internal/surface"
)

// ptln daemon — Epic R remote-launch (MVP). A resident agent the web can ASK to launch a
// pre-registered, owner-approved party agent in a registered project dir, so coordinating
// devs' LLMs needs no per-launch commands. Full plan + threat model: docs/DAEMON-MVP.md.
//
// SECURITY INVARIANT (the whole design rests on this): the control plane sends a REFERENCE
// (a project/profile LABEL), never a command. Only resolveLaunch — against the LOCAL,
// owner-authored registry — turns a reference into an argv. No field from a reference is
// ever used as a path or an argv fragment; labels are matched EXACTLY against the registry,
// so "../../etc" or any injection simply fails to resolve.
//
// R0 (this file): the local registry + the resolver + detached spawn + a LOCAL trigger
// (`daemon launch-local`) to prove resolve→spawn end-to-end. No outbound stream or web
// endpoint yet (R1/R3), so there is no remote surface to attack at this slice.

func daemonMain(args []string) {
	if len(args) == 0 {
		daemonUsage()
		return
	}
	switch args[0] {
	case "enable":
		daemonEnable(args[1:])
	case "run":
		daemonRun()
	case "disable":
		daemonDisable()
	case "install":
		daemonInstall()
	case "restart":
		daemonRestart()
	case "stop":
		daemonStop()
	case "uninstall":
		daemonUninstall()
	case "requests":
		daemonRequests()
	case "kill":
		daemonKill(args[1:])
	case "launch-request":
		daemonLaunchRequest(args[1:])
	case "add-project":
		daemonAddProject(args[1:])
	case "remove-project":
		daemonRemoveProject(args[1:])
	case "repos":
		daemonRepos()
	case "destinations", "dests":
		daemonDestinations()
	case "scan-root":
		daemonScanRoot(args[1:])
	case "consults":
		daemonConsultsPolicy(args[1:]) // per-project auto-answer switch (read-only Q&A only)
	case "deliver":
		daemonDeliverPolicy(args[1:]) // per-project: may a peer's ANSWER be auto-submitted here
	case "projects", "status", "ls":
		daemonStatus()
	case "launch-local":
		daemonLaunchLocal(args[1:])
	case "set-github-token":
		daemonSetGitHubToken(args[1:])
	case "provision":
		daemonProvision(args[1:])
	case "autoupdate":
		daemonAutoUpdate(args[1:])
	case "state":
		daemonStateMain() // machine-readable status (JSON) — what the tray companion polls
	case "doctor":
		daemonDoctor()
	case "-h", "--help", "help":
		daemonUsage()
	default:
		fmt.Fprintf(os.Stderr, "ptln daemon: unknown subcommand %q\n", args[0])
		daemonUsage()
		os.Exit(2)
	}
}

func daemonUsage() {
	fmt.Println(`Usage: ptln daemon <command>   (Epic R remote-launch — MVP, in progress)

  enable [device-label]                             enrol this machine (needs ptln login); mints a
                                                    device token. Defaults the label to the hostname.
  run                                               connect + listen; an interactive confirm console
                                                    (approve/deny <id> · approve-run/deny-run · kill · list)
  disable                                           revoke the device token + remove it locally
  install                                           run the daemon ALWAYS-ON as a per-user OS
                                                    service (launchd/systemd) — survives closing
                                                    the manager + reboots. Needs 'enable' first.
  restart                                           restart the always-on service in place
                                                    (re-execs the same binary; no reinstall)
  uninstall                                         stop + remove the always-on service

  requests                                          list pending launch requests for this device
  kill <request-id>                                 stop a launched agent (SIGTERM) + record it
  add-project <label> [dir] [--preset spec|chat]    register a project the web may launch into
              [--engine claude|codex|gemini|antigravity]  (dir defaults to the current directory)
  remove-project <label>                            unregister
  repos                                             list the local git repos this machine offers to
                                                    the web (Settings → Integrations → Repository
                                                    access → Local directory)
  destinations                                      list the directories this machine will CLONE a
                                                    web-assigned project into (always includes
                                                    ~/partyline; add more with scan-root add)
  consults <label> [auto|ask]                       whether a teammate's READ-ONLY question about this
                                                    project is answered without asking you (default
                                                    auto). Nothing to do with launching write work —
                                                    that stays on the project's Ask/Auto policy.
  consults --all [auto|ask]                         the same switch for the WHOLE MACHINE. '--all ask'
                                                    is the off switch: it outranks every project's
                                                    setting, takes effect immediately (no restart,
                                                    always-on service included), and persists.
  deliver <label> [stage|submit]                    when a peer's answer lands for a session in this
                                                    project: stage it in your prompt (default) or
                                                    submit it to your agent for you. Submit is OFF by
                                                    default on purpose (your agent can write; the
                                                    read-only rule protects the ANSWERING machine).
  status                                            show this device + registered projects
  launch-request --party <id> --daemon <id>         (dev/test) ask a daemon to launch a project,
            --label <label> [--preset spec|chat]    as the logged-in user — the web "Add agent" UI in R4
  launch-local <label> '<party-link>'               R0 spike: resolve + spawn locally (no server)
  set-github-token [--from-gh|--clear]              store a LOCAL GitHub token for PR creation
                                                    (reads the token from stdin; never leaves this box)
  stop                                              stop the always-on service but KEEP it installed
                                                    (restart brings it back; uninstall removes it)
  provision [on|off|status]                         make this box a provisioned worker the web can
                                                    hand build capacity to (see the docs)
  state                                             daemon slice of 'ptln state' (JSON)
  doctor                                            check every crank prerequisite + how to fix each
  autoupdate [on|off]                               keep this daemon current: while idle it upgrades
                                                    itself to new releases (off by default; needs
                                                    'install', since a service must restart into it)

State lives under ~/.partyline/daemon/ (registry.json + device.json) and never leaves this
machine — only project LABELS mirror to the web for an "Add agent" picker. Run it in the
foreground with 'run', or 'install' it as an always-on background service.`)
}

// ---- local registry (owner-authored; absolute paths NEVER leave the machine) ----

type daemonProject struct {
	Label  string `json:"label"`
	Path   string `json:"path"`             // absolute dir; local only
	Preset string `json:"preset"`           // "spec" | "chat" — the launch flavor
	Policy string `json:"policy,omitempty"` // "ask" (approve each launch AND run) | "auto"; "" == AUTO (see launchPolicy)
	Engine string `json:"engine,omitempty"` // claude|codex|gemini|antigravity; "" == claude (the party default)
	// Consults governs AUTO-ANSWERING a peer's read-only question, and NOTHING else. Separate from
	// Policy on purpose: Policy grants unattended WRITE runs, this grants a paragraph of read-only
	// feedback, and one used to imply the other. "ask" (the per-project OFF) queues consults for the
	// owner; "" == auto. See consult_policy.go for why the default is on.
	Consults string `json:"consults,omitempty"` // "auto" | "ask"; "" == auto
	// Deliver governs the ASKING side: what happens to a peer's answer when it lands for a session
	// working in this project. "stage" ("" default) pastes it into the prompt unsubmitted; "submit"
	// presses Enter too. A THIRD privilege, separate from both of the above, because it is the only
	// one where untrusted text becomes a prompt in an agent that can write. See peer_deliver.go.
	Deliver string `json:"deliver,omitempty"` // "stage" | "submit"; "" == stage
}

// launchPolicy normalizes a project's approval policy. Default is "auto": REGISTRATION IS THE
// CONSENT. `ptln daemon add-project <label> <dir>` is an affirmative act by this machine's owner
// declaring the directory available to their team; asking again at every launch asks the same
// question twice, with no new information in the second ask.
//
// It also could not work. The prompt only ever rendered in the daemon's interactive console or
// the mux TUI banner — neither of which exists when the daemon runs as the always-on service,
// which is the normal install. Requests piled up in an in-memory map that nothing displayed and
// no notification mentioned, and a restart dropped them. "Awaiting approval" meant "lost".
//
// Unattended work is the point: a job runs on whichever machine a teammate picks. "ask" stays
// available for a project explicitly marked so, for anyone who wants a human in the loop.
func (p daemonProject) launchPolicy() string {
	if p.Policy == "ask" {
		return "ask"
	}
	return "auto"
}

type daemonRegistry struct {
	Projects []daemonProject `json:"projects"`
	// ScanRoots are EXTRA directories this machine advertises repositories from, beyond $HOME —
	// for machines whose code lives on a data mount (/mnt/data, /srv). Owner-authored local
	// state, same posture as project paths: the roots themselves never leave the machine.
	ScanRoots []string `json:"scan_roots,omitempty"`
}

func daemonRegistryPath() string { return filepath.Join(daemonDir(), "registry.json") }

func loadDaemonRegistry() daemonRegistry {
	var r daemonRegistry
	if b, err := os.ReadFile(daemonRegistryPath()); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

func saveDaemonRegistry(r daemonRegistry) error {
	if err := os.MkdirAll(filepath.Dir(daemonRegistryPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(daemonRegistryPath(), b, 0o600)
}

var labelRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9 _.-]{0,47}$`)

// runIDRe pins a run id to a plain UUID (the shape of runs.id). --run is a server value appended
// to crank's argv AFTER resolveRun, so it must be exactly a UUID — nothing that could carry a
// flag, a path fragment, whitespace, or a newline into the argv.
var runIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func daemonAddProject(args []string) {
	fs := flag.NewFlagSet("add-project", flag.ExitOnError)
	preset := fs.String("preset", "spec", "launch flavor: spec | chat")
	engine := fs.String("engine", "", "agent engine: claude|codex|gemini|antigravity (default claude)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal(fmt.Errorf("usage: ptln daemon add-project <label> [dir] [--preset spec|chat] [--engine <e>]"))
	}
	*engine = strings.ToLower(strings.TrimSpace(*engine))
	if *engine != "" && !validEngine(*engine) {
		fatal(fmt.Errorf("--engine must be one of claude, codex, gemini, antigravity"))
	}
	label := strings.TrimSpace(fs.Arg(0))
	if !labelRe.MatchString(label) {
		fatal(fmt.Errorf("invalid label %q (letters/digits/space/._- , ≤48 chars)", label))
	}
	dir := fs.Arg(1)
	if dir == "" {
		dir, _ = os.Getwd()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal(fmt.Errorf("resolve %q: %w", dir, err))
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		fatal(fmt.Errorf("%s is not a directory", abs))
	}
	if *preset != "spec" && *preset != "chat" {
		fatal(fmt.Errorf("--preset must be spec or chat"))
	}
	reg := loadDaemonRegistry()
	for i := range reg.Projects {
		if reg.Projects[i].Label == label { // update in place
			reg.Projects[i].Path, reg.Projects[i].Preset, reg.Projects[i].Engine = abs, *preset, *engine
			if err := saveDaemonRegistry(reg); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ updated project %q → %s (%s, %s)\n", label, abs, *preset, engineLabel(*engine))
			syncIfEnrolled()
			return
		}
	}
	reg.Projects = append(reg.Projects, daemonProject{Label: label, Path: abs, Preset: *preset, Engine: *engine})
	if err := saveDaemonRegistry(reg); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ registered project %q → %s (%s, %s)\n", label, abs, *preset, engineLabel(*engine))
	syncIfEnrolled()
}

// validEngine reports whether e is a known party engine (the internal/engine registry).
func validEngine(e string) bool { return eng.Valid(e) }

// engineLabel is the display name for a project's engine ("" → "claude", the default).
func engineLabel(e string) string { return eng.Label(e) }

func daemonRemoveProject(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln daemon remove-project <label>"))
	}
	label := args[0]
	reg := loadDaemonRegistry()
	kept := reg.Projects[:0]
	found := false
	for _, p := range reg.Projects {
		if p.Label == label {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	reg.Projects = kept
	if err := saveDaemonRegistry(reg); err != nil {
		fatal(err)
	}
	if found {
		fmt.Printf("✓ removed project %q\n", label)
		syncIfEnrolled()
	} else {
		fmt.Printf("(no project labeled %q)\n", label)
	}
}

// daemonConsultsPolicy is the per-project OFF (and back ON) for consult auto-answer. Purely local: it
// edits this machine's registry and syncs nothing, because the control plane never learns a policy —
// it can't, or the asker's side of the wire would know whether it was about to be auto-approved.
func daemonConsultsPolicy(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln daemon consults <label>|--all [auto|ask]"))
	}
	label := args[0]
	if label == "--all" || label == "all" {
		daemonConsultsGlobalPolicy(args[1:])
		return
	}
	reg := loadDaemonRegistry()
	idx := -1
	for i := range reg.Projects {
		if reg.Projects[i].Label == label {
			idx = i
			break
		}
	}
	if idx < 0 {
		fatal(fmt.Errorf("no project labeled %q on this machine (see `ptln daemon status`)", label))
	}
	if len(args) == 1 { // read
		if reg.Projects[idx].consultPolicy() == "auto" {
			// nil = no project setting in view here: the CLI reads local state only, so it reports this
			// machine's own fallback. The project's setting (web) may raise or lower it per question.
			fmt.Printf("%s: consults answered automatically (read-only, up to %d/day unless the project sets its own)\n", label, consultAutoDailyCap(nil))
		} else {
			fmt.Printf("%s: consults wait for your approval\n", label)
		}
		noteGlobalConsultsOff()
		return
	}
	switch args[1] {
	case "auto", "on":
		reg.Projects[idx].Consults = "auto"
	case "ask", "off":
		reg.Projects[idx].Consults = "ask"
	default:
		fatal(fmt.Errorf("usage: ptln daemon consults %s [auto|ask]", label))
	}
	if err := saveDaemonRegistry(reg); err != nil {
		fatal(err)
	}
	if reg.Projects[idx].consultPolicy() == "auto" {
		fmt.Printf("✓ %s: teammates' read-only questions are answered without asking you (%d/day here unless the project sets its own, then they queue)\n", label, consultAutoDailyCap(nil))
		noteGlobalConsultsOff()
		return
	}
	fmt.Printf("✓ %s: teammates' read-only questions wait for you (`approve-consult <id>`, or ctrl-\\ p)\n", label)
}

// noteGlobalConsultsOff keeps the per-project surface honest: with the machine-wide switch off, a
// project reading "answered automatically" would be a lie, since the global OFF outranks it.
func noteGlobalConsultsOff() {
	if globalConsultPolicy() == "auto" {
		return
	}
	fmt.Println("  ⚠ but auto-answer is OFF for this whole machine, which wins — every question waits for you.")
	fmt.Println("    turn it back on with: ptln daemon consults --all auto")
}

// daemonConsultsGlobalPolicy is the MACHINE-WIDE off switch for consult auto-answer, and the one that
// actually works on the common install. It writes local state the answering process re-reads per
// question (consult_policy.go), so it needs no restart and no reinstall — unlike an env var, which the
// installed launchd/systemd unit never inherited from the shell that set it.
func daemonConsultsGlobalPolicy(args []string) {
	if len(args) == 0 { // read
		if globalConsultPolicy() == "auto" {
			fmt.Println("all projects: auto-answer is ALLOWED on this machine — each project decides")
			fmt.Println("  turn it off everywhere with: ptln daemon consults --all ask")
		} else {
			fmt.Println("all projects: auto-answer is OFF on this machine — every teammate's question waits for you")
			fmt.Println("  (" + consultGlobalPolicyPath() + ")")
		}
		return
	}
	var mode string
	switch args[0] {
	case "auto", "on":
		mode = "auto"
	case "ask", "off":
		mode = "ask"
	default:
		fatal(fmt.Errorf("usage: ptln daemon consults --all [auto|ask]"))
	}
	if err := setGlobalConsultPolicy(mode); err != nil {
		fatal(fmt.Errorf("could not write the machine-wide consult setting: %w", err))
	}
	if mode == "ask" {
		fmt.Println("✓ auto-answer OFF for this whole machine — no teammate can make it run a turn unasked.")
		fmt.Println("  Every question queues for you (`ptln peer approve <id>`, or ctrl-\\ p), including for")
		fmt.Println("  projects set to auto: the machine-wide OFF outranks any per-project setting.")
		fmt.Println("  In effect immediately, for the always-on service too — no restart, no reinstall.")
		return
	}
	fmt.Println("✓ auto-answer allowed on this machine again — each project's own setting decides")
	fmt.Println("  (`ptln daemon consults <label> ask` turns it off for one project; `ptln settings` shows the effective state)")
}

// daemonDeliverPolicy is the asking side's switch: when a peer's answer lands for a session working in
// this project, is it staged in the prompt (default) or submitted for you? Local only, like the others.
func daemonDeliverPolicy(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln daemon deliver <label> [stage|submit]"))
	}
	label := args[0]
	reg := loadDaemonRegistry()
	idx := -1
	for i := range reg.Projects {
		if reg.Projects[i].Label == label {
			idx = i
			break
		}
	}
	if idx < 0 {
		fatal(fmt.Errorf("no project labeled %q on this machine (see `ptln daemon status`)", label))
	}
	if len(args) == 1 {
		if reg.Projects[idx].Deliver == "submit" {
			fmt.Printf("%s: a peer's answer is SUBMITTED to your agent automatically\n", label)
		} else {
			fmt.Printf("%s: a peer's answer is staged in your prompt; you press ⏎\n", label)
		}
		return
	}
	switch args[1] {
	case "submit", "on":
		reg.Projects[idx].Deliver = "submit"
	case "stage", "off":
		reg.Projects[idx].Deliver = "stage"
	default:
		fatal(fmt.Errorf("usage: ptln daemon deliver %s [stage|submit]", label))
	}
	if err := saveDaemonRegistry(reg); err != nil {
		fatal(err)
	}
	if reg.Projects[idx].Deliver == "submit" {
		fmt.Printf("⚠ %s: a peer's answer will be SUBMITTED to your agent with no keystroke from you.\n", label)
		fmt.Println("  That agent has whatever tools you launched it with — the read-only rule applies to the")
		fmt.Println("  machine ANSWERING, not to yours. A teammate's text becomes a prompt here, unattended.")
		return
	}
	fmt.Printf("✓ %s: a peer's answer is staged in your prompt for you to read and send\n", label)
}

func daemonInstall() {
	note, err := installService()
	if err != nil {
		fatal(err)
	}
	fmt.Println("✓ always-on: " + note)
	fmt.Println("  the daemon now runs in the background + across reboots — close the manager freely.")
}

// daemonRestart restarts the always-on service in place (re-execs the SAME installed binary via
// launchd/systemd — no reinstall, no code fetched). If there's no service, it explains the
// options rather than pretending to restart a foreground `ptln daemon run` (which has no
// supervisor to bring it back).
// daemonStop pauses this machine: the service stops accepting new work but stays INSTALLED, so
// `ptln daemon restart` (or the next login/reboot) brings it back. Use `uninstall` to remove it.
func daemonStop() {
	if !serviceInstalled() {
		fmt.Println("No always-on service to stop.")
		fmt.Println("  • Foreground `ptln daemon run`? Stop it with Ctrl-C.")
		return
	}
	if !serviceActive() {
		fmt.Println("Already stopped — `ptln daemon restart` to start it again.")
		return
	}
	if err := stopService(); err != nil {
		fatal(err)
	}
	fmt.Println("✓ daemon stopped — it stays installed; `ptln daemon restart` starts it again.")
	fmt.Println("  Runs already in flight keep going (they're detached and report on their own).")
}

func daemonRestart() {
	if !serviceInstalled() {
		fmt.Println("No always-on service to restart.")
		fmt.Println("  • Foreground `ptln daemon run`? Stop it (Ctrl-C) and run it again.")
		fmt.Println("  • Want it supervised (survives reboots, restartable)? `ptln daemon install`.")
		return
	}
	if err := restartService(); err != nil {
		fatal(err)
	}
	fmt.Println("✓ daemon service restarted.")
}

func daemonUninstall() {
	if !serviceInstalled() {
		fmt.Println("always-on is not installed — nothing to remove.")
		return
	}
	if err := uninstallService(); err != nil {
		fatal(err)
	}
	fmt.Println("✓ always-on removed — the background daemon is stopped.")
}

// acquireRunLock takes an exclusive, non-blocking flock so only ONE daemon (the always-on
// service OR a foreground `daemon run`) holds the stream at a time. Two consumers would both
// act on `accepted` events — the single-use join ref stops a double spawn, but the loser would
// mark the request failed. The fd is held for the process lifetime. ok=false ⇒ another holds it.
func acquireRunLock() (ok bool, release func()) {
	noop := func() {}
	path := filepath.Join(daemonDir(), "run.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return true, noop // can't create the dir — don't keep the daemon from running over a lock
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true, noop
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return false, noop
	}
	return true, func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
}

func daemonStatus() {
	if d := loadDaemonDevice(); d.Token != "" {
		fmt.Printf("device:    enabled (id %s) → %s\n", d.DaemonID, d.Base)
		// Owner-mismatch guard: a daemon is owned by whoever ran `enable`. If you've since logged in
		// as a different account, this daemon is INVISIBLE in your fleet — surface it here instead of
		// a silently empty fleet. Best-effort; a network hiccup just skips the check.
		if _, email, err := api.DaemonOwner(d.Base, d.Token); err == nil && email != "" {
			if acct := strings.TrimSpace(api.LoadAccount().Email); acct != "" && !strings.EqualFold(acct, email) {
				fmt.Printf("  ⚠ owned by %s — but you're logged in as %s, so it won't show in your fleet.\n", email, acct)
				fmt.Println("    re-enrol for this account:  ptln daemon disable && ptln daemon enable && ptln daemon restart")
			}
		}
	} else {
		fmt.Println("device:    not enabled — `ptln daemon enable`")
	}
	if serviceInstalled() {
		state := "installed"
		if serviceActive() {
			state = "running"
		}
		fmt.Printf("always-on: %s (%s)\n", state, serviceUnitPath())
	} else {
		fmt.Println("always-on: off — `ptln daemon install` to run in the background")
	}
	reg := loadDaemonRegistry()
	if len(reg.Projects) == 0 {
		fmt.Println("projects: none — `ptln daemon add-project <label> [dir]`")
		return
	}
	fmt.Printf("projects (%s):\n", daemonRegistryPath())
	for _, p := range reg.Projects {
		fmt.Printf("  %-20s %-6s %s\n", p.Label, p.Preset, p.Path)
	}
}

// ---- device enrolment (the device-scoped token; separate from the login token) ----

// daemonDevice is the local credential this machine uses to hold its outbound stream open.
// The token is device-scoped and revocable, so a resident daemon never carries the login
// token. Stored 0600; like the registry, it never leaves the machine except as the token
// on the wire to the control plane it was minted by.
type daemonDevice struct {
	DaemonID string `json:"daemon_id"`
	Token    string `json:"token"`
	Base     string `json:"base"`
}

func daemonDevicePath() string { return filepath.Join(daemonDir(), "device.json") }

func loadDaemonDevice() daemonDevice {
	var d daemonDevice
	if b, err := os.ReadFile(daemonDevicePath()); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return d
}

func saveDaemonDevice(d daemonDevice) error {
	if err := os.MkdirAll(filepath.Dir(daemonDevicePath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(daemonDevicePath(), b, 0o600)
}

// isAuthErr reports whether a control-plane error is an auth failure, so callers can show
// "run `ptln login`" instead of leaking a raw "unauthenticated". Covers the stale/expired
// local token case (LoadToken != "" but the server rejects it).
func isAuthErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unauthenticated") || strings.Contains(s, "unauthorized")
}

// defaultDeviceLabel is the hostname, which is how the machine appears in the fleet.
func defaultDeviceLabel() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return strings.TrimSpace(h)
	}
	return "device"
}

// enrollDevice registers this machine with the control plane and saves the device credential.
// The non-fatal core of `daemon enable`, shared with `ptln setup`.
func enrollDevice(label string) (daemonDevice, error) {
	id, token, err := api.New().RegisterDaemon(label)
	if err != nil {
		return daemonDevice{}, err
	}
	d := daemonDevice{DaemonID: id, Token: token, Base: api.Base()}
	if err := saveDaemonDevice(d); err != nil {
		return daemonDevice{}, err
	}
	return d, nil
}

func daemonEnable(args []string) {
	if api.LoadToken() == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login` first"))
	}
	if d := loadDaemonDevice(); d.Token != "" {
		// A revoked credential used to dead-end here: the crash-loop error said "run `ptln daemon
		// enable` again" and enable refused because device.json still existed, so the error's own
		// instruction didn't work. Verify the token before refusing — a definite auth rejection
		// means revoked, so re-enrol in place (fresh device id). Anything else (healthy, network
		// down) keeps the refusal: we can't tell healthy from revoked without the control plane,
		// and replacing a working enrolment on a flaky connection would silently fork the fleet.
		if deviceRevoked(d) {
			fmt.Println("⚠ this machine's device token was revoked — re-enrolling with a fresh device id…")
		} else {
			fmt.Printf("daemon already enabled (device %s). `ptln daemon run` to start it,\nor `ptln daemon disable` first to re-enrol.\n", d.DaemonID)
			return
		}
	}
	label := defaultDeviceLabel()
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		label = strings.TrimSpace(args[0])
	}
	d, err := enrollDevice(label)
	if err != nil {
		if isAuthErr(err) {
			fatal(fmt.Errorf("not logged in (or your session expired) — run `ptln login` first"))
		}
		fatal(fmt.Errorf("register: %w", err))
	}
	fmt.Printf("✓ daemon enabled as %q (device %s)\n", label, d.DaemonID)
	fmt.Println("  run it always-on:  ptln daemon install   (or foreground: ptln daemon run)")
}

// pendingLaunch is a launch request awaiting the owner's confirm. As of S3 the pending event
// carries NO join ref — the ref is minted server-side at accept time and arrives with the
// `accepted` event. Approving just flips the request to `accepted`; executeAccepted does the
// spawn when the accepted event lands (decoupling approval from execution).
type pendingLaunch struct {
	label  string
	preset string
	party  string
}

// daemonAuthorize flips a pending request to `accepted` — the owner's "yes". It does NOT
// spawn: execution happens later in executeAccepted, on the `accepted` event, so the CLI and
// the web modal converge on one execution path ("first surface to authorize wins"). Guards
// against authorizing a label this machine doesn't actually have.
func daemonAuthorize(d daemonDevice, label, reqID string) error {
	if projectByLabel(loadDaemonRegistry(), label) == nil {
		_ = api.SetLaunchStatus(d.Base, d.Token, reqID, "declined", "unknown project: "+label)
		return fmt.Errorf("no project %q in this machine's registry", label)
	}
	return api.SetLaunchStatus(d.Base, d.Token, reqID, "accepted", "")
}

// isAutoProject reports whether the LOCAL registry marks this label Auto (instant launch, no
// prompt). Policy lives only on this machine — the server never sees it (S2 decision).
//
// THIS IS THE WRITE-WORK GATE. It answers "may a teammate's request spawn an autonomous agent that
// changes this checkout". It is NOT the gate for answering a read-only question — that is
// autoAnswerConsults (consult_policy.go), a strictly weaker privilege with its own setting. Don't
// reintroduce the conflation by reusing this for a consult.
func isAutoProject(label string) bool {
	p := projectByLabel(loadDaemonRegistry(), label)
	return p != nil && p.launchPolicy() == "auto"
}

// autoRunProject reports whether this project drains QUEUED runs without anyone pressing Start.
//
// NOT launchPolicy. Those are two different consents and collapsing them is what made the Start
// button meaningless: launchPolicy answers "may my machine be used at all", and its default is auto
// because registration is the affirmative act. Whether a queued run STARTS is a different question,
// answered by a human on the board — and defaulting THAT to auto means a task dropped in the backlog
// begins building immediately, so there is nowhere to put work you have not decided to start yet.
//
// So this requires the registry to say "auto" EXPLICITLY. An unmarked project queues and waits; a
// project its owner deliberately marked auto drains unattended, which is the behaviour someone opts
// into for a backlog they want chewed through.
// autoRunProject reports whether a RUN on this project may start without a console approval.
//
// It defers to launchPolicy for one reason: the two gates were inverted. launchPolicy defaults to
// AUTO ("registration is the consent") and lets a project opt into "ask"; this one defaulted to ASK
// and required an explicit opt-in. So a directory the owner had already registered would run party
// launches unattended but park every crank run behind a prompt — the same question, answered two
// different ways by the same daemon.
//
// The argument in launchPolicy's comment applies here verbatim and is not merely a preference: the
// approve-run prompt renders ONLY in the interactive console or the mux TUI banner, and neither
// exists when the daemon runs as the always-on service (the normal install). Pending runs sat in an
// in-memory map that nothing displayed, no notification mentioned, and a restart dropped. Observed
// 2026-08-14: 14 restarted runs sat at "Starting…" indefinitely with nothing anywhere to approve
// them — the web board said Building, the daemon was waiting, and the two never met.
//
// "ask" still opts a project back in, for anyone who wants a human in the loop AND is running the
// daemon in a foreground console where the prompt can actually be seen.
func autoRunProject(label string) bool {
	p := projectByLabel(loadDaemonRegistry(), label)
	return p != nil && p.launchPolicy() != "ask"
}

// executeAccepted is the SOLE place a request becomes a running agent, triggered by an
// `accepted` event (from any surface). Idempotent for the single-stream case: a local launch
// record short-circuits a reconnect re-push, and the server's single-use join ref is the
// backstop. Exchange the ref → resolve against the local registry → spawn detached → mark
// spawned. Each failure records a reason for the audit trail. Returns the log path on success.
func executeAccepted(d daemonDevice, ev api.LaunchEvent) (logPath string, err error) {
	if _, ok := loadLaunchRecords()[ev.RequestID]; ok {
		return "", nil // already spawned (reconnect re-pushed the accepted event) — no-op
	}
	reg := loadDaemonRegistry()
	fail := func(stage string, e error) (string, error) {
		_ = api.SetLaunchStatus(d.Base, d.Token, ev.RequestID, "failed", stage+": "+e.Error())
		return "", fmt.Errorf("%s: %w", stage, e)
	}
	if projectByLabel(reg, ev.ProjectLabel) == nil {
		return fail("resolve", fmt.Errorf("unknown project %q", ev.ProjectLabel))
	}
	link, _, err := api.ExchangeJoinRef(d.Base, d.Token, ev.RequestID, ev.PartyJoinRef)
	if err != nil {
		return fail("exchange", err)
	}
	argv, dir, err := resolveLaunch(reg, launchRef{ProjectLabel: ev.ProjectLabel, PartyLink: link, Engine: ev.Engine, Grants: ev.ToolGrants})
	if err != nil {
		return fail("resolve", err)
	}
	pid, logPath, err := spawnLaunch(argv, dir, ev.ProjectLabel)
	if err != nil {
		return fail("spawn", err)
	}
	recordLaunch(ev.RequestID, pid, ev.ProjectLabel)
	_ = api.SetLaunchStatus(d.Base, d.Token, ev.RequestID, "spawned", "")
	return logPath, nil
}

// runRefFromEvent maps a stream RunEvent to the resolver's runRef (O.2). It is a pure field
// copy — the mapping seam between the wire shape and resolveRun, so a test can pin that a
// RunEvent's fields reach the security chokepoint intact without spawning anything.
func runRefFromEvent(ev api.RunEvent) runRef {
	return runRef{
		ProjectLabel: ev.ProjectLabel,
		ThreadID:     ev.ThreadID,
		RunID:        ev.RunID, // scopes the worklist file to this run — see runRef.RunID
		Tasks:        ev.Tasks,
		Preset:       ev.Preset,
		VisualVerify: ev.VisualVerify,
		VisualRoutes: ev.VisualRoutes,
		// G.6: the project's pipeline policy. Data only — see runRef.Checks.
		Checks:      ev.Checks,
		ReviewLanes: ev.ReviewLanes,
	}
}

// augmentRunArgv appends the run-owned scalars to a resolved crank argv, AFTER resolveRun and
// deliberately NOT through it (the resolver stays the pure label→path chokepoint). Both values
// are server-supplied ints/ids, never paths: --run must be a plain UUID (rejecting anything that
// could smuggle a flag/path/whitespace into the argv), and --max-tokens (#81 slice 3b) is only
// appended when the budget is a positive int — 0/absent means unbounded, so the flag is omitted
// and crank runs without a ceiling (the pre-slice-3b behaviour). Pure + total, so a test can pin
// the argv without spawning crank.
//
// #81 slice 3c: --resume is ALWAYS appended (after --run). On a fresh run the run store has no
// `done` tasks so it's a no-op; on a re-queued (operator-approved) run it skips the tasks already
// `done` so an approved run resumes where it paused instead of re-doing completed work.
func augmentRunArgv(argv []string, ev api.RunEvent) ([]string, error) {
	if !runIDRe.MatchString(ev.RunID) {
		return nil, fmt.Errorf("invalid run id %q", ev.RunID)
	}
	// Continue vs Restart: a normal re-dispatch resumes where it stopped (--resume: skip done tasks,
	// resume-in-place from the stored session). A web "Restart" (ev.Restart) starts the run over —
	// --restart makes crank rebuild each task's worktree+branch fresh and ignore prior done/blocked
	// state. Exactly one of the two is passed.
	argv = append(argv, "--run", ev.RunID)
	if ev.Restart {
		argv = append(argv, "--restart")
	} else {
		argv = append(argv, "--resume")
	}
	if ev.MaxTokens > 0 {
		argv = append(argv, "--max-tokens", strconv.Itoa(ev.MaxTokens))
	}
	// #569: the project's bound on the in-run repair loop. Same posture as --max-tokens — a run-owned
	// scalar appended after resolveRun, never through it. Fail-closed: only an in-range int is passed;
	// anything else (absent, negative, over the ceiling) is dropped so crank keeps its own default.
	if n, ok := validMaxRepairs(ev.MaxRepairRounds); ok {
		argv = append(argv, "--max-repairs", strconv.Itoa(n))
	}
	// #77 slice 3: per-run merge policy. Only pr/auto are passed (manual is crank's no-op default),
	// and only the two known values — never arbitrary server text into the argv.
	if ev.MergePolicy == "pr" || ev.MergePolicy == "auto" {
		argv = append(argv, "--merge-policy", ev.MergePolicy)
	}
	// Review gate ON for this org → open the PR as a DRAFT; the human's Accept marks it ready. A plain
	// boolean the server sets (reference-not-command). Absent/false (review off) → a normal PR, since
	// there's no Accept step to un-draft it. Old daemons ignore --draft's absence identically.
	if ev.ReviewRequired {
		argv = append(argv, "--draft")
	}
	// Model selection: forward the run's build model to crank. STRICTLY validated (a plain model token —
	// alnum start, then alnum/dot/dash/underscore) so nothing server-supplied can smuggle a flag, path,
	// whitespace, or newline into the argv. An empty/invalid value is simply omitted → engine default.
	if modelRe.MatchString(ev.Model) {
		argv = append(argv, "--model", ev.Model)
	}
	// Chain's shared branch: same posture as --model — a plain token, validated against branchRe before
	// it reaches the argv, so nothing server-supplied can smuggle a flag/path/newline. crank re-slugs it.
	if branchRe.MatchString(ev.ChainBranch) {
		argv = append(argv, "--branch", ev.ChainBranch)
	}
	// Project base branch: the fork point AND the PR target. Same posture as --branch — validated
	// against branchRe before it reaches the argv. Omitted when unset/invalid, which leaves crank on
	// the repo's own default branch (the pre-setting behavior).
	if branchRe.MatchString(ev.BaseBranch) {
		argv = append(argv, "--base", ev.BaseBranch)
	}
	// Engine (Epic #73): forward the run's build engine to crank. Registry-keys-only (validEngine),
	// so nothing injection-shaped reaches the argv; claude/empty is omitted (crank's default).
	if validEngine(ev.Engine) && engineLabel(ev.Engine) != "claude" {
		argv = append(argv, "--engine", ev.Engine)
	}
	// Active git provider: only pr/auto runs act on a branch, and only gitlab/bitbucket change the
	// merge step (github is crank's default, so it's omitted). Strict enum allowlist — never arbitrary
	// server text into the argv.
	if ev.GitProvider == "gitlab" || ev.GitProvider == "bitbucket" {
		argv = append(argv, "--git-provider", ev.GitProvider)
	}
	return argv, nil
}

// validMaxRepairs is the fail-closed gate on the run's --max-repairs value (#569). Only an integer
// in [0, maxRepairRoundsCeiling] is honored; nil (the column is NULL) or anything out of range
// returns crank's own default with ok=false, so the CALLER omits the flag entirely rather than
// passing a value it doesn't trust. Pure + total so the validation table can be pinned in a test.
func validMaxRepairs(v *int) (int, bool) {
	if v == nil || *v < 0 || *v > maxRepairRoundsCeiling {
		return defaultMaxRepairRounds, false
	}
	return *v, true
}

// modelRe bounds a model token to a safe shape for the argv: must start alphanumeric (so it can never
// look like a flag), then alnum / dot / dash / underscore, up to 60 chars. Covers claude aliases
// (opus/sonnet/haiku/fable), dated ids (claude-opus-4-8), and other engines' names; rejects anything
// with a slash, space, or leading dash.
var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$`)

// branchRe pins a chain's shared branch name to a safe git-ref shape: starts alnum (so a leading "-"
// can't smuggle a flag), then alnum/dot/dash/underscore/slash only — no whitespace, "..", or anything
// a shell or argv could interpret. Same chokepoint posture as modelRe; gitwt re-slugs it anyway.
var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,119}$`)

// seedRunTasks writes the run's worklist to the per-task store as `queued` (#77 slice 2) BEFORE
// launching crank in claim mode — the workers claim these rows atomically (slice 1). idx is the
// task's position in ev.Tasks, so the idx a worker later reports done/failed against lines up.
// Idempotent (upsert by run_id,idx) and best-effort: a failed seed logs and continues (a task that
// never seeds simply never gets claimed). Tasks are DATA (ev.Tasks), never argv.
func seedRunTasks(d daemonDevice, ev api.RunEvent) {
	// Seed ONLY missing rows. The task upsert overwrites status by design (crank re-reports
	// transitions), so blind-seeding `queued` here would clobber a `done` row on every re-dispatch
	// of a resumed run — and a claimed re-queued task is rebuilt at full model cost. An existing
	// row, whatever its status, is the store's truth; a failed read seeds nothing new (claim then
	// just finds fewer queued tasks, never re-runs finished ones).
	existing := map[int]bool{}
	if rows, err := api.ListRunTasks(d.Base, d.Token, ev.RunID); err == nil {
		for _, r := range rows {
			existing[r.Idx] = true
		}
	}
	for i, task := range ev.Tasks {
		if existing[i] {
			continue
		}
		if err := api.UpsertRunTask(d.Base, d.Token, ev.RunID, api.RunTaskUpdate{Idx: i, Task: task, Status: "queued"}); err != nil {
			fmt.Fprintf(os.Stderr, "  (seed run-task %d: %v)\n", i, err)
		}
	}
}

// spawnRun is the run-profile twin of executeAccepted (O.2): a QUEUED run becomes a running
// `crank` ONLY by resolving the reference against the LOCAL registry. resolveRun is the sole
// chokepoint — it exact-matches the label, validates the thread id, and writes the tasks as a
// worklist file (DATA, never argv). Nothing server-supplied becomes a path or a flag. On a clean
// spawn it flips the run to `running` and returns the UNWAITED process; the caller decides whether
// to wait inline (the serial run-queue) or in a goroutine (fire-and-forget). If the daemon restarts
// mid-run the detached crank survives but its terminal transition is lost (the child is orphaned) —
// acceptable for O.2; O.3's per-task store is the durable lifecycle record.
func spawnRun(d daemonDevice, ev api.RunEvent) (*exec.Cmd, error) {
	reg := loadDaemonRegistry()
	fail := func(stage string, e error) error {
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", stage+": "+e.Error())
		return fmt.Errorf("%s: %w", stage, e)
	}
	argv, dir, err := resolveRun(reg, runRefFromEvent(ev))
	// PROVISIONED fallback (docs/plans/provisioned-workers.md, P2): the label isn't in the local
	// registry, but the run was dispatched as provisioned AND this node opted into provision mode —
	// clone the repo on demand and run in a managed dir. resolveRun above stays the untouched registry
	// chokepoint (it always wins); provisionRun is the SECOND, opt-in path with its own validation.
	if err != nil && ev.Provisioned && provisionEnabled() {
		argv, dir, err = provisionRun(d, ev)
	}
	if err != nil {
		return nil, fail("resolve", err) // unknown label / bad thread id / no tasks / provision failure — refused, no spawn
	}
	// Run-owned scalars (--run, --max-tokens) are appended AFTER resolveRun — never through it,
	// so the resolver stays the pure label→path chokepoint. A bad run id refuses the whole run.
	argv, err = augmentRunArgv(argv, ev)
	if err != nil {
		return nil, fail("run id", err)
	}
	// Phase B3: hand crank the project globals (projects.document, carried on the event) as a file it
	// writes into each task's worktree as AGENTS.md + CLAUDE.md. Best-effort: a write failure logs and runs WITHOUT
	// injected globals rather than failing the run — missing context degrades a run, it doesn't break it.
	// Safe filename: ev.RunID was validated by augmentRunArgv above.
	if strings.TrimSpace(ev.Globals) != "" {
		if gf, gerr := writeRunGlobals(ev.RunID, ev.Globals); gerr == nil {
			argv = append(argv, "--globals-file", gf)
		} else {
			fmt.Fprintf(os.Stderr, "  (run %s: globals write failed, running without project globals: %v)\n", ev.RunID, gerr)
		}
	}
	// Anchor-triggered recall: the thread's recorded knowledge about the files these tasks name,
	// resolved server-side. Same best-effort posture as globals — a worker without it is a worker
	// that has to rediscover what someone already learned, which is slow, not broken.
	if strings.TrimSpace(ev.AnchoredContext) != "" {
		if cf, cerr := writeRunContext(ev.RunID, ev.AnchoredContext); cerr == nil {
			argv = append(argv, "--context-file", cf)
		} else {
			fmt.Fprintf(os.Stderr, "  (run %s: context write failed, running without anchored context: %v)\n", ev.RunID, cerr)
		}
	}
	// #575: build-role tool grants ride a daemon-written file (DATA, parallel to --globals-file);
	// the worker re-validates + resolves them locally before anything widens. Best-effort — a write
	// failure runs without grants, never fails the run.
	if ev.ToolGrants != nil {
		if gf, gerr := writeRunGrants(ev.RunID, ev.ToolGrants); gerr == nil {
			argv = append(argv, "--grants-file", gf)
		} else {
			fmt.Fprintf(os.Stderr, "  (run %s: grants write failed, running without tool grants: %v)\n", ev.RunID, gerr)
		}
	}
	// Org skill library: fetch this org's ENABLED skills and stage them for crank to inject into each
	// task's worktree (--skills-dir, parallel to --globals-file). Best-effort like globals — a fetch or
	// write failure logs and the run proceeds with NO skills, never fails. RunID was validated above.
	if skills, serr := api.SkillsForDaemon(d.Base, d.Token); serr != nil {
		fmt.Fprintf(os.Stderr, "  (run %s: skills fetch failed, running without org skills: %v)\n", ev.RunID, serr)
	} else if len(skills) > 0 {
		// PACKAGE skills: for each skill that ships a bundle, fetch its full zip so crank can materialize
		// the WHOLE tree (scripts/assets) into each worktree. Best-effort per skill — a bundle fetch
		// failure just means that skill degrades to body-only injection, never a failed run.
		bundles := map[string][]byte{}
		for _, s := range skills {
			if !s.HasBundle {
				continue
			}
			if zip, berr := api.GetSkillBundleForDaemon(d.Base, d.Token, s.Name); berr == nil {
				bundles[s.Name] = zip
			} else if !errors.Is(berr, api.ErrSkillNoBundle) {
				fmt.Fprintf(os.Stderr, "  (run %s: skill %q bundle fetch failed, injecting body-only: %v)\n", ev.RunID, s.Name, berr)
			}
		}
		if sd, werr := writeRunSkills(ev.RunID, skills, bundles); werr == nil {
			argv = append(argv, "--skills-dir", sd)
			// Usage telemetry (cheap tier): report which skills we INJECTED into this run so the library
			// shows "injected into N runs". Best-effort — a report failure never affects the build.
			refs := make([]api.SkillRef, 0, len(skills))
			for _, s := range skills {
				refs = append(refs, api.SkillRef{Name: s.Name, Version: s.Version})
			}
			if uerr := api.ReportSkillUsage(d.Base, d.Token, ev.RunID, refs); uerr != nil {
				fmt.Fprintf(os.Stderr, "  (run %s: skill-usage report failed (non-fatal): %v)\n", ev.RunID, uerr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "  (run %s: skills stage failed, running without org skills: %v)\n", ev.RunID, werr)
		}
	}
	// #77 slice 2: fleet/claim mode is OPT-IN via PARTYLINE_CRANK_WORKERS (an operator sets fleet
	// width per machine). When on, crank claims tasks from the run store (atomic, slice 1) instead
	// of the worklist file — so many workers here + on other org boxes chew one run concurrently.
	// We seed the store (queued) from ev.Tasks first so there's something to claim. When OFF (the
	// default), nothing changes: the file-mode path (and the #81 pause/approve loop) run as before,
	// with NO dependency on migration 0041.
	if strings.TrimSpace(os.Getenv("PARTYLINE_CRANK_WORKERS")) != "" {
		argv = append(argv, "--claim")
		seedRunTasks(d, ev)
	}
	// The merge train (land.go), opt-in PER MACHINE the same way fleet width is: verified work goes
	// straight onto the base branch instead of waiting in a PR, so later tasks fork from a base that
	// already contains the earlier ones. Deliberately an ENV VAR and not a field on the run event —
	// "may this machine write to my main branch" is a property of the machine and its owner, not of
	// whoever enqueued the run, and the reference-not-command invariant says the control plane does
	// not get to decide it. Unset (the default) → crank never lands, exactly as before.
	if envOn("PARTYLINE_CRANK_LAND") {
		argv = append(argv, "--land")
	}
	// crank self-reports per-task lifecycle (O.3) via the device token + base, passed through the
	// child's env (the token never appears in argv). Reuse PARTYLINE_API for the base.
	runEnv := []string{"PARTYLINE_RUN_ID=" + ev.RunID, "PARTYLINE_DAEMON_TOKEN=" + d.Token}
	if d.Base != "" {
		runEnv = append(runEnv, "PARTYLINE_API="+d.Base)
	}
	// Log under the (validated) project label, consistent with launches — never the RunID.
	cmd, _, err := startDetached(argv, dir, ev.ProjectLabel, runEnv)
	if err != nil {
		return nil, fail("spawn", err)
	}
	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "running", "")
	trackRun(ev.RunID, cmd.Process.Pid) // so a web cancel can SIGTERM this process group
	return cmd, nil
}

// waitRun blocks until a spawned crank exits and records the terminal run status.
// budgetPauseExit (3) is crank's "paused, needs approval" signal on an unattended run that hit the
// token ceiling — NOT a failure; verifyPauseExit (Trust · T3) is crank finishing with ≥1 task
// quarantined by a verify gate. Both route to needs_approval so the web surfaces + notifies; any
// other non-zero exit is a failure.
func waitRun(d daemonDevice, ev api.RunEvent, cmd *exec.Cmd) {
	werr := cmd.Wait()
	untrackRun(ev.RunID) // the child is gone — nothing left for a kill to signal
	var ee *exec.ExitError
	switch {
	case errors.As(werr, &ee) && ee.ExitCode() == budgetPauseExit:
		// G.2: the exit code already knows WHY. It used to be discarded here, leaving four
		// unrelated pauses indistinguishable downstream — which is what made a rate-limited run,
		// needing no action at all, render identically to a quarantine that needs a human.
		_ = api.SetRunStatusReason(d.Base, d.Token, ev.RunID, "needs_approval", surface.PauseBudget,
			"token budget reached — approve more or stop")
	case errors.As(werr, &ee) && ee.ExitCode() == verifyPauseExit:
		_ = api.SetRunStatusReason(d.Base, d.Token, ev.RunID, "needs_approval", surface.PauseQuarantine,
			"verification failed on one or more tasks — review the quarantined branches, then approve or stop")
	case errors.As(werr, &ee) && ee.ExitCode() == rateLimitExit:
		// Provider rate limit: crank ALREADY self-reported needs_approval with the reset time (which
		// it has and we don't). Leave the status alone — re-writing it here would drop the reset time.
	case errors.As(werr, &ee) && ee.ExitCode() == resumeAbortExit:
		// Resume abort: crank couldn't read the task store and refused to rebuild finished work
		// blind. It ALREADY self-reported `failed` with the actual fetch error — leave it alone so
		// the run page names the cause instead of a bare exit status.
	case errors.As(werr, &ee) && ee.ExitCode() == emptyWorklistExit:
		// The worklist parsed to zero tasks — nothing ran. A FAILURE with a named cause, not the
		// silent `done` this used to record (chain c842d926).
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", "the worklist had no tasks — nothing ran; check the run's task list")
	case werr != nil:
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", "crank: "+werr.Error())
	default:
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "done", "")
	}
}

// executeRun spawns crank and waits in a goroutine (fire-and-forget). executeRunSync spawns and
// waits INLINE — used by the serial run-queue worker so exactly ONE crank runs at a time on this
// machine (no thrashing five agents + the API limits when a batch is Started from the web).
func executeRun(d daemonDevice, ev api.RunEvent) error {
	if ev.Preset == "describe" {
		go func() { _ = runJob(d, ev, runDescribeJob) }()
		return nil
	}
	if ev.Preset == "review" {
		go func() { _ = runJob(d, ev, runReviewJob) }()
		return nil
	}
	if ev.Preset == "rebase" {
		go func() { _ = runJob(d, ev, runRebaseJob) }()
		return nil
	}
	cmd, err := spawnRun(d, ev)
	if err != nil {
		return err
	}
	go waitRun(d, ev, cmd)
	return nil
}

func executeRunSync(d daemonDevice, ev api.RunEvent) error {
	if ev.Preset == "describe" {
		return runJob(d, ev, runDescribeJob)
	}
	if ev.Preset == "review" {
		return runJob(d, ev, runReviewJob)
	}
	if ev.Preset == "rebase" {
		return runJob(d, ev, runRebaseJob)
	}
	cmd, err := spawnRun(d, ev)
	if err != nil {
		return err
	}
	waitRun(d, ev, cmd)
	return nil
}

// runDescribeJob is the daemon side of the one-shot web describe (preset "describe"). It does NOT spawn
// crank — the idea is turned into a scored work item by a single local engine turn (runDescribeToWorkItem)
// and filed into the planning tree. The project label is resolved through resolveRun — the SAME label→path
// chokepoint as a crank run — so nothing server-supplied becomes a path; we take only the validated dir.
// Records via api.New() (the USER's ~/.partyline/token), since the daemon runs on the user's own machine
// and a work item must belong to the person, not the device. Terminal status lets the web reflect it.
func runDescribeJob(d daemonDevice, ev api.RunEvent) error {
	fail := func(stage string, e error) error {
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", stage+": "+e.Error())
		return fmt.Errorf("%s: %w", stage, e)
	}
	reg := loadDaemonRegistry()
	_, dir, err := resolveRun(reg, runRefFromEvent(ev)) // validate label→path; argv is unused for describe
	if err != nil {
		return fail("resolve", err)
	}
	if len(ev.Tasks) == 0 || strings.TrimSpace(ev.Tasks[0]) == "" {
		return fail("describe", fmt.Errorf("no idea to describe"))
	}
	client := api.New()
	if client.Token == "" {
		return fail("describe", fmt.Errorf("not logged in on this machine — run `ptln login`"))
	}
	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "running", "")
	// Live step output (crank-01): stream the agent's work to run_logs so the web tails it like a crank
	// run — the daemon holds the device token directly (not via env), so wire the logger with explicit
	// creds. close() flushes the tail before we set the terminal status.
	logger := newRunLoggerWith(d.Base, d.Token, ev.RunID)
	// ev.Globals (Phase B3) carries the project document — fold it into the decomposition so the plan
	// respects the project's stack/conventions/guardrails and its notion of a "buildable" task.
	// ev.Model is the project's plan model (server-sent, headed for an exec argv) — same modelRe
	// shape gate as augmentRunArgv; empty/invalid is omitted → the engine's host default.
	model := ""
	if modelRe.MatchString(ev.Model) {
		model = ev.Model
	}
	// Engine (Epic #73): server-sent when valid > this machine's per-project engine > claude —
	// the same pecking order as resolveLaunch. The notice makes an override/ignore auditable.
	engineName, note := resolveRunEngine(reg, ev.ProjectLabel, ev.Engine)
	if note != "" {
		if sink := logger.sink(0); sink != nil {
			sink(note)
		}
	}
	// Org skill library — DESCRIBE is read-only PLANNING in the LIVE project dir, so we do NOT
	// materialize skill files here (that would pollute the real repo). Instead the ENABLED skills ride
	// in as a manifest folded into the project-context (globals) channel, so the planner specs tasks
	// knowing which capabilities the BUILDERS (crank, which DOES materialize them) will have. Best-
	// effort: a fetch failure just omits the manifest.
	globals := ev.Globals
	if skills, serr := api.SkillsForDaemon(d.Base, d.Token); serr == nil {
		if m := skillManifest(skills); m != "" {
			globals = strings.TrimRight(globals, "\n") + "\n\n" + m
		}
	}
	summary, err := runDescribeToWorkItem(client, ev.ThreadID, dir, engineName, ev.Tasks[0], globals, model, logger.sink(0), 4*time.Minute)
	logger.close()
	if err != nil {
		return fail("describe", err)
	}
	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "done", summary)
	return nil
}

// daemonRun holds the outbound stream open (reconnect w/ backoff, clean SIGINT/SIGTERM) AND
// runs an interactive confirm console — the "CLI confirm-queue first" model. Incoming launch
// requests are queued; nothing spawns until the owner types `approve <id>` here. This is the
// human-in-the-loop gate: the control plane only ever sends a label, and a label becomes a
// running command only after a local registry match + this explicit approval.
func daemonRun() {
	d := loadDaemonDevice()
	if d.Token == "" {
		fatal(fmt.Errorf("daemon not enabled — run `ptln daemon enable` first"))
	}
	ok, release := acquireRunLock()
	if !ok {
		fatal(fmt.Errorf("another partyline daemon is already running (the always-on service, or another `ptln daemon run`)"))
	}
	defer release()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Advertise current projects so a teammate's "Add agent" picker is up to date (R2).
	if err := mirrorProjects(d); err != nil {
		fmt.Printf("⚠ couldn't mirror projects (%v)\n", err)
	}

	// #267: heartbeat — keep last_seen fresh (so a long-lived stream never reads as stale) and
	// mirror a metadata-only config snapshot for the fleet map. Best-effort; a failed beat just
	// means the web marks this daemon stale a little sooner. Fires now, then every 60s until ctx.
	go func() {
		beat := func() { _ = api.Heartbeat(d.Base, d.Token, daemonConfigSnapshot()) }
		beat()
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				beat()
			}
		}
	}()

	// Session invites → the menu bar. The daemon is the right place for this ONLY because it already
	// holds a connection and is meant to be always-on: `ptln state` (which the tray shells every 4s)
	// stays network-free, and reads the file this writes. See invites_cache.go for the full why.
	//
	// Uses the USER's token, not the device token — an invite is addressed to a person, and the
	// device token authenticates a machine. On a machine whose owner is signed out this simply does
	// nothing (refreshInviteCache returns early), which is the correct answer rather than an error.
	go func() {
		uc := api.New()
		poll := func() { refreshInviteCache(uc, time.Now()) }
		poll()
		t := time.NewTicker(invitePollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				poll()
			}
		}
	}()

	// #683: measure each project's environment delta ("what is built but not live yet") with plain
	// git and report it. Read-only observation of repos this machine already has, on its own slower
	// ticker than the heartbeat — it shells out to git per branch, and shipping state changes on the
	// scale of merges, not seconds. Best-effort throughout: a failure means the web shows the
	// previous numbers with their original timestamp, never a wrong number.
	go func() {
		t := time.NewTimer(envReportInitial)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				reportEnvDeltas(d)
				t.Reset(envReportEvery)
			}
		}
	}()

	// Crash recovery for in-process jobs (describe/review/rebase): synchronous, BEFORE the stream
	// can dispatch anything — every job record on disk right now belongs to a dead process.
	sweepOrphanJobs(d)

	// Crash recovery: reconcile runs this machine was building when it last went down (a reboot or
	// kill mid-crank strands them `running` server-side forever) — AND keep reconciling every minute.
	// The recurring pass exists because a daemon RESTART (self-update, upgrade, crash) while a
	// detached crank child is alive strands a run in a way the one-shot startup sweep can't see: the
	// sweep finds the pid ALIVE and keeps the record, the child finishes minutes later, and the exit
	// watcher (waitRun) that would have flipped the run died with the old process — so the run sits
	// `running` forever with its work complete. Only a recurring sweep observes that completion.
	go func() {
		sweepOrphanRuns(d)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepOrphanRuns(d)
			}
		}
	}()

	// Bring up the menu bar icon alongside the daemon: the moment this machine starts accepting work
	// is the moment its status is worth seeing. Automatic and best-effort — honors `ptln tray off`,
	// no-ops when a tray is already up (the tray's own lock), and can never block the daemon.
	wakeTray()

	// Opt-in self-update: keeps an always-on node from drifting weeks behind the fleet. Every guard
	// (opted in, service-managed, idle, genuinely newer) is re-checked per tick — see autoupdate.go.
	startAutoUpdate(ctx)

	var mu sync.Mutex
	pending := map[string]pendingLaunch{}
	pendingRuns := map[string]api.RunEvent{} // O.2: queued runs awaiting the owner's approve-run
	// ask_peer: consults awaiting the owner's yes. DURABLE (consult_pending.go): this used to be an
	// in-memory map, so a restart dropped every peer's question and the asker timed out with no
	// explanation. The file is a cache; the control plane's rows are the truth, which is why we
	// reconcile from them below as well as taking live pushes off the stream.
	consults := newConsultQueue(pendingConsultsPath())
	startedRuns := map[string]bool{} // run ids currently in the run-queue — dedup so a
	// stream reconnect that re-pushes the same accepted run (before spawnRun flips it to `running`
	// server-side) can't double-enqueue. An id is REMOVED when its execution finishes: a run that
	// quarantined (needs_approval) and is later resumed from the web flips back to `accepted` and is
	// re-pushed on this same long-lived connection — holding its id forever would silently swallow
	// every resume until the service restarts (which is exactly what happened to a run parked for
	// two days next to a healthy daemon). Post-execution the server status is no longer `accepted`,
	// so nothing re-pushes until a human genuinely re-accepts — which must re-enqueue.

	// Serial run-queue (concurrency 1). EVERY run this daemon executes without a human waiting at the
	// console — web Start (`go`), Auto projects, AND console approve-run — goes through this one
	// worker, so we never spawn five cranks at once and thrash the machine + API limits when a batch
	// is Started from the web. The worker runs one crank to completion, then takes the next; order is
	// the board order (the stream pushes accepted runs by backlog_rank). A failure / needs_approval
	// does NOT stop the queue — the worker just proceeds to the next run (Slice-2 chains add the only
	// exception: a chained run stays un-eligible until its predecessors are `done`, gated server-side).
	//
	// REVIEWS GET THEIR OWN LANE. The web already exempts them from the team concurrency cap
	// (presetUnderCap: "a reviewer must NEVER be counted toward, or withheld by, the build cap") —
	// but sharing this one worker put the starvation right back one layer down. A review dispatched
	// behind a long build waited for it, so three finished builds sat un-reviewed for an hour while
	// the queue chewed through a compile. A reviewer is a fresh, tool-less engine reading a diff: it
	// costs almost nothing next to a build, and holding it behind one is pure latency.
	//
	// Separate queues, so a review never waits on a build and vice versa. Sizes are env-tunable for a
	// beefy machine; the BUILD default stays 1 (unchanged behaviour — builds are the heavy, API-limit
	// -bound work), while reviews default to 2 because they're cheap and latency is what hurts.
	// BUILD concurrency is now a live limit, not a fixed worker count — see run_limiter.go. An
	// explicit PARTYLINE_RUN_WORKERS is the machine owner's override and wins; otherwise the team's
	// JOBS PER MACHINE arrives on run events and sets it (0/unset keeps the old serial default of 1).
	buildOverride := 0
	if n := envWorkers("PARTYLINE_RUN_WORKERS", 0); n > 0 {
		buildOverride = n
	}
	buildLimit := newRunLimiter(maxInt(buildOverride, 1))
	reviewLimit := newRunLimiter(envWorkers("PARTYLINE_REVIEW_WORKERS", 2))
	exec := func(ev api.RunEvent) {
		if err := executeRunSync(d, ev); err != nil {
			fmt.Printf("\n✗ run %s failed (%v)\n> ", ev.RunID, err)
		}
		mu.Lock()
		delete(startedRuns, ev.RunID)
		mu.Unlock()
	}
	runQueue := make(chan queuedJob, 512)
	reviewQueue := make(chan queuedJob, 512)
	go drainRunQueueLimited(runQueue, buildLimit)
	go drainRunQueueLimited(reviewQueue, reviewLimit)
	enqueueRun := func(ev api.RunEvent) {
		mu.Lock()
		if startedRuns[ev.RunID] {
			mu.Unlock()
			return
		}
		startedRuns[ev.RunID] = true
		delete(pendingRuns, ev.RunID) // if it was also awaiting console approval, it's the queue's now
		mu.Unlock()
		// Adopt the team's JOBS PER MACHINE before dispatching. A local override always wins — the
		// person at the machine knows what it can take — and 0 means the server has no opinion, so
		// the existing limit stands rather than silently dropping to 1.
		if buildOverride == 0 && ev.MaxPerMachine > 0 {
			buildLimit.setLimit(ev.MaxPerMachine)
		}
		// Route to the lane that matches the work: a review can't be stuck behind a build.
		q := runQueue
		if ev.Preset == "review" {
			q = reviewQueue
		}
		job := queuedJob{run: func() { exec(ev) }}
		select {
		case q <- job:
		default:
			// Queue full (extreme — 512 buffered) — drop the dedup mark so the stream's next re-push
			// of this still-`accepted` run gets another chance rather than being lost.
			mu.Lock()
			delete(startedRuns, ev.RunID)
			mu.Unlock()
		}
	}

	fmt.Printf("daemon running (device %s) → %s\n", d.DaemonID, d.Base)
	fmt.Println("  console:  approve <id>  ·  deny <id>  ·  approve-run <id>  ·  deny-run <id>  ·  approve-consult <id>  ·  deny-consult <id>  ·  kill <id>  ·  list  ·  ctrl-c to stop")
	go daemonConsole(d, &mu, pending, pendingRuns, consults, enqueueRun)

	// Recover questions that arrived while this process was DOWN (nothing pushed them — there was no
	// stream) and re-affirm the ones on disk against the durable rows. Best-effort: the list endpoint
	// is user-authed, so a daemon enrolled by device token alone skips this and relies on the stream's
	// reconnect re-push. Announced when it finds something, because it means work was waiting.
	if n := reconcileConsults(consults, d.DaemonID, api.New()); n > 0 {
		fmt.Printf("↺ recovered %d pending consult(s) — `list` to see them\n", n)
	}

	// The local control channel: same-host processes (the `ctrl-\ p` modal, the tray) ask THIS daemon
	// to approve or deny a consult, because only the daemon can answer one — see daemon_control.go.
	stopControl := startDaemonControl(ctx, d, consults)
	defer stopControl()

	backoff := time.Second
	for {
		err := api.DaemonStream(ctx, d.Base, d.Token,
			func() { backoff = time.Second; fmt.Println("● connected") },
			func(ev api.LaunchEvent) { // pending — notify the owner (or auto-accept an Auto project)
				if isAutoProject(ev.ProjectLabel) {
					if err := daemonAuthorize(d, ev.ProjectLabel, ev.RequestID); err != nil {
						fmt.Printf("\n⚠ auto-accept %s failed (%v)\n> ", ev.RequestID, err)
					} else {
						fmt.Printf("\n⚡ auto-accepted %s — %q (Auto)\n> ", ev.RequestID, ev.ProjectLabel)
					}
					return
				}
				if announcePendingLaunch(&mu, pending, ev) {
					fmt.Printf("\n📨 launch request %s — %q (%s)\n   approve %s   ·   deny %s\n> ",
						ev.RequestID, ev.ProjectLabel, ev.Preset, ev.RequestID, ev.RequestID)
				}
			},
			func(ev api.LaunchEvent) { // accepted (by any surface) — execute
				clearPending(&mu, pending, ev.RequestID)
				if logPath, err := executeAccepted(d, ev); err != nil {
					fmt.Printf("\n✗ %s failed (%v)\n> ", ev.RequestID, err)
				} else if logPath != "" {
					fmt.Printf("\n✓ launched %q (%s)\n  log: %s\n> ", ev.ProjectLabel, ev.RequestID, logPath)
				}
			},
			func(ev api.RunEvent) { // O.2 run-profile — same ask/auto gate as a launch
				// B + serial queue: a web-accepted run (`go`) or an Auto project's run is handed to the
				// serial run-queue (concurrency 1) — it runs when the worker is free, no console needed.
				// This is what lets an always-on/headless daemon run web-Started work, and what makes a
				// batch run one-at-a-time instead of thrashing. enqueueRun dedups reconnect re-pushes.
				if ev.Go {
					enqueueRun(ev)
					fmt.Printf("\n▶ queued run %s — %q (%d task(s)) from the web\n> ", ev.RunID, ev.ProjectLabel, len(ev.Tasks))
					return
				}
				if autoRunProject(ev.ProjectLabel) {
					enqueueRun(ev)
					fmt.Printf("\n⚡ queued run %s — %q (Auto, %d task(s))\n> ", ev.RunID, ev.ProjectLabel, len(ev.Tasks))
					return
				}
				if announcePendingRun(&mu, pendingRuns, startedRuns, ev) {
					fmt.Printf("\n📋 run request %s — %q (%s, %d task(s))\n   approve-run %s   ·   deny-run %s\n> ",
						ev.RunID, ev.ProjectLabel, ev.Preset, len(ev.Tasks), ev.RunID, ev.RunID)
				}
			},
			func(reqID string) { // web kill — SIGTERM the child + record it
				killLaunch(d, reqID, "killed from web")
				clearPending(&mu, pending, reqID)
				fmt.Printf("\n🛑 stopped %s (kill requested)\n> ", reqID)
			},
			func(runID string) { // web kill of a RUN — actually stop the crank work
				if killRun(runID) {
					fmt.Printf("\n🛑 stopped run %s (cancelled from web)\n> ", runID)
				}
			},
			func(runID string) { // web PAUSE of a RUN — SIGSTOP the crank group, hold it mid-flight
				if pauseRun(runID) {
					fmt.Printf("\n⏸  paused run %s (held from web)\n> ", runID)
				}
			},
			func(runID string) { // web RESUME of a paused RUN — SIGCONT the crank group
				if resumeRun(runID) {
					fmt.Printf("\n▶  resumed run %s (continued from web)\n> ", runID)
				}
			},
			func() { // owner-triggered restart from the web (#3). Only if we ARE the supervised
				// service — a foreground `ptln daemon run` has no supervisor to bring it back, so
				// we ignore rather than kill it. Clean exit → launchd KeepAlive / systemd
				// Restart=always relaunches the SAME installed binary (no code fetched).
				if !serviceInstalled() {
					fmt.Printf("\n↻ restart requested from web, but this isn't the always-on service — ignoring (install it with `ptln daemon install`).\n> ")
					return
				}
				fmt.Println("\n↻ restart requested from the web — exiting for the supervisor to relaunch…")
				release() // drop the run lock so the relaunched instance can acquire it
				os.Exit(0)
			},
			func(ev api.RelabelEvent) { // projects rename cascade: rename our own registry entry + re-mirror.
				if err := relabelProject(d, ev.OldLabel, ev.NewLabel); err != nil {
					fmt.Printf("\n⚠ rename %q→%q failed (%v)\n> ", ev.OldLabel, ev.NewLabel, err)
					return
				}
				fmt.Printf("\n🏷  renamed project %q → %q from the web\n> ", ev.OldLabel, ev.NewLabel)
			},
			func(ev api.BindRepoEvent) { // Repository access → Local directory / "put this repo on that box"
				if ev.DestinationHandle != "" {
					// A CLONE assignment: it can take minutes, so it never runs on the stream loop —
					// a blocked callback would stall every other event on this connection.
					go handleAssignment(d, ev)
					return
				}
				if err := bindLocalRepo(ev); err != nil {
					fmt.Printf("\n⚠ bind %q failed (%v)\n> ", ev.Label, err)
					go reportAssignment(d, ev.AssignmentID, "failed", err.Error())
					return
				}
				if ev.AssignmentID != "" {
					// Same 'ready' rule as a clone, and the same gate: registered AND re-advertised.
					// Off the stream loop — the heartbeat retries and must not block other events.
					go func() {
						if err := readvertise(d); err != nil {
							reportAssignment(d, ev.AssignmentID, "failed",
								fmt.Sprintf("%s is registered on this machine, but it couldn't tell partyline (%v) — it will appear on the next heartbeat (within a minute)", ev.Label, err))
							return
						}
						reportAssignment(d, ev.AssignmentID, "ready", "")
					}()
				}
				fmt.Printf("\n📂 registered project %q from a local repository (web)\n> ", ev.Label)
			},
			func() { // web-requested self-update — run the guarded upgrade off the stream loop (the
				// download takes a while; a successful one restarts the service out from under us).
				fmt.Printf("\n⟳ update requested from the web — checking…\n> ")
				go webUpdateTick()
			},
			func(ev api.ConsultEvent) { // ask_peer P0.c: a teammate asks THIS daemon for read-only feedback.
				// Three outcomes, in this order, and the order matters:
				//   1. we don't advertise the label → decline, so the asker isn't left waiting
				//   2. the label auto-answers AND today's budget has room → one read-only turn, no human
				//   3. otherwise → the approval queue, where a human decides (the pre-auto-answer path,
				//      still intact and still the only way a question gets answered from a UI)
				// The gate is the CONSULT policy, not isAutoProject: answering a question is a far weaker
				// privilege than launching a write run, and it now has its own setting (consult_policy.go).
				// Either way the answer is ONE read-only engine turn on our own checkout (P0.0), never a
				// write and never a command.
				if projectByLabel(loadDaemonRegistry(), ev.ProjectLabel) == nil {
					// We don't advertise this label (removed/relabeled since) — decline so the asker isn't
					// left waiting. Never answer about a project we can't resolve locally. FIRST, ahead of
					// any auto-answer decision: an unresolvable label is not a question we hold at all.
					go func() {
						_ = api.DeclineConsult(d.Base, d.Token, ev.ConsultID, "this machine doesn't have that project")
					}()
					return
				}
				// EVERY consult lands in the pending set first, including one we're about to auto-answer.
				// The set is the dedupe: the stream's push-once memory is per-connection, so a reconnect
				// re-pushes anything still pending server-side. The auto-answer path used to skip this and
				// spawn straight from the event, which means a flapping connection could run the same
				// answer turn repeatedly — free before, billable now that a turn is also a budget charge.
				// Add-then-Take makes the auto path idempotent by construction, and a crash between the
				// two leaves the question in the queue for a human, which is the safe way to fail.
				if !consults.Add(ev, time.Now()) {
					return // already known — a reconnect re-push, not a new question
				}
				// decideConsult takes the label and a SPEND NUMBER, deliberately: nothing from ev that the
				// asker controls — not a flag, not the question, not the label's spelling — can steer a
				// request onto the no-human path. ConsultAutoDaily is the PROJECT's allowance, resolved
				// server-side from project settings so one web edit governs every machine in the project;
				// it bounds how much we spend, never whether a human is skipped, and the daemon clamps it
				// to its own ceiling. See consult_policy.go / consult_budget.go.
				capped := ""
				if verdict, why := decideConsult(ev.ProjectLabel, ev.ConsultAutoDaily); verdict == consultVerdictAutoAnswer {
					taken, got := consults.Take(ev.ConsultID)
					if !got {
						return // a human decided it between the Add and here — theirs, not ours
					}
					// Take is atomic, so a human approving in the same instant can't also spawn a turn.
					fmt.Printf("\n💬 consult %s — %s about %q (auto-answering read-only)\n> ", ev.ConsultID, consultAsker(ev), ev.ProjectLabel)
					go answerConsult(d, taken)
					return
				} else if why != "" {
					// Over budget: fall THROUGH to the human queue rather than dropping the question or
					// erroring the asker. The allowance is spent; the owner can still say yes.
					capped = " — " + why
				}
				{ // queued for the owner — announce it once (Add already deduped the re-push)
					q := ev.Question
					if len(q) > 160 {
						q = q[:160] + "…"
					}
					// Name the HUMAN. This line used to read `"partyline" asks: …` — the %q was the
					// project label, so the project appeared to be asking and the person was nowhere.
					// Approving an anonymous question is a poor decision to put in front of anyone, and
					// it is the same missing identity consult_budget.go wanted for per-asker keying.
					fmt.Printf("\n💬 consult request %s — %s asks about %q%s: %s\n   approve-consult %s   ·   deny-consult %s   ·   or answer it in ctrl-\\ p\n> ",
						ev.ConsultID, consultAsker(ev), ev.ProjectLabel, capped, q, ev.ConsultID, ev.ConsultID)
				}
			},
			func(ev api.ConsultAnswerEvent) { // ask_peer: a peer answered a consult WE asked. P0.d injects this
				// into the asking session as untrusted feedback; for P0.a we surface it on the console.
				fmt.Printf("\n📬 consult %s answered (about %q):\n%s\n> ", ev.ConsultID, ev.ProjectLabel, ev.Answer)
			},
			func(ev api.ConsultCancelEvent) { // ask_peer: the ASKER withdrew a question we were holding.
				// Drop it, so we stop offering the owner an approve that would burn a read-only turn producing
				// an answer the control plane will refuse. An answer turn already IN FLIGHT is deliberately
				// left alone — it finishes and the control plane discards the answer (consult_withdraw.go).
				if _, held := consults.Withdraw(ev.ConsultID); held {
					fmt.Printf("\n🚫 consult %s withdrawn by the asker (about %q) — nothing to answer\n> ",
						ev.ConsultID, ev.ProjectLabel)
				}
			})
		if ctx.Err() != nil {
			fmt.Println("\nstopped.")
			return
		}
		if errors.Is(err, api.ErrDaemonRevoked) {
			// `enable` self-heals a revoked credential (re-enrols in place), so this instruction
			// actually works now — it used to bounce off "already enabled … disable first".
			fatal(fmt.Errorf("device token revoked — run `ptln daemon enable`, then `ptln daemon restart`"))
		}
		fmt.Printf("… disconnected (%v); reconnecting in %s\n", err, backoff)
		select {
		case <-ctx.Done():
			fmt.Println("stopped.")
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// daemonConsole reads owner commands from stdin and is the ONLY place a received request
// turns into a spawned agent (after approve). Runs for the life of `daemon run`.
func daemonConsole(d daemonDevice, mu *sync.Mutex, pending map[string]pendingLaunch, pendingRuns map[string]api.RunEvent, consults *consultQueue, enqueue func(api.RunEvent)) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			fmt.Print("> ")
			continue
		}
		switch f[0] {
		case "list", "ls":
			waiting := consults.List()
			mu.Lock()
			if len(pending) == 0 && len(pendingRuns) == 0 && len(waiting) == 0 {
				fmt.Println("(no pending requests)")
			}
			for id, p := range pending {
				fmt.Printf("  launch  %s  %q (%s)\n", id, p.label, p.preset)
			}
			for id, r := range pendingRuns {
				fmt.Printf("  run     %s  %q (%s, %d task(s))\n", id, r.ProjectLabel, r.Preset, len(r.Tasks))
			}
			mu.Unlock()
			for _, c := range waiting {
				fmt.Printf("  consult %s  %q (read-only feedback, waiting %s)\n",
					c.Event.ConsultID, c.Event.ProjectLabel, shortDuration(time.Since(c.SeenAt)))
			}
		case "approve", "y":
			if len(f) < 2 {
				fmt.Println("usage: approve <id>")
			} else {
				daemonApprove(d, mu, pending, f[1])
			}
		case "deny", "n":
			if len(f) < 2 {
				fmt.Println("usage: deny <id>")
			} else {
				daemonDeny(d, mu, pending, f[1])
			}
		case "approve-run":
			if len(f) < 2 {
				fmt.Println("usage: approve-run <id>")
			} else {
				daemonApproveRun(d, mu, pendingRuns, f[1], enqueue)
			}
		case "deny-run":
			if len(f) < 2 {
				fmt.Println("usage: deny-run <id>")
			} else {
				daemonDenyRun(d, mu, pendingRuns, f[1])
			}
		case "approve-consult":
			if len(f) < 2 {
				fmt.Println("usage: approve-consult <id>")
			} else {
				daemonApproveConsult(d, consults, f[1])
			}
		case "deny-consult":
			if len(f) < 2 {
				fmt.Println("usage: deny-consult <id>")
			} else {
				daemonDenyConsult(d, consults, f[1])
			}
		case "kill", "stop":
			if len(f) < 2 {
				fmt.Println("usage: kill <id>")
			} else if killLaunch(d, f[1], "killed by owner") {
				fmt.Printf("✓ killed %s\n", f[1])
			} else {
				fmt.Printf("no local process for %s (already gone?)\n", f[1])
			}
		case "quit", "exit":
			os.Exit(0)
		default:
			fmt.Println("commands: approve <id> · deny <id> · approve-run <id> · deny-run <id> · approve-consult <id> · deny-consult <id> · kill <id> · list · quit")
		}
		fmt.Print("> ")
	}
}

// daemonApprove authorizes a pending request (flips it to `accepted`). It does NOT spawn —
// the `accepted` event that follows drives executeAccepted, so a web approval and this CLI
// approval take the identical execution path. Clears the local pending entry on success.
func daemonApprove(d daemonDevice, mu *sync.Mutex, pending map[string]pendingLaunch, id string) {
	mu.Lock()
	p, ok := pending[id]
	mu.Unlock()
	if !ok {
		fmt.Printf("no pending request %q (try `list`)\n", id)
		return
	}
	if err := daemonAuthorize(d, p.label, id); err != nil {
		fmt.Printf("✗ approve %s failed (%v)\n", id, err)
		clearPending(mu, pending, id)
		return
	}
	clearPending(mu, pending, id)
	fmt.Printf("✓ approved %s — launching %q…\n", id, p.label)
}

func daemonDeny(d daemonDevice, mu *sync.Mutex, pending map[string]pendingLaunch, id string) {
	mu.Lock()
	_, ok := pending[id]
	mu.Unlock()
	if !ok {
		fmt.Printf("no pending request %q\n", id)
		return
	}
	if err := api.SetLaunchStatus(d.Base, d.Token, id, "declined", "declined by owner"); err != nil {
		fmt.Printf("deny failed (%v)\n", err)
		return
	}
	clearPending(mu, pending, id)
	fmt.Printf("✓ declined %s\n", id)
}

func clearPending(mu *sync.Mutex, pending map[string]pendingLaunch, id string) {
	mu.Lock()
	delete(pending, id)
	mu.Unlock()
}

// announcePendingLaunch records a launch awaiting the owner's console approve/deny and reports
// whether this is its FIRST sighting — so the caller prints the approve/deny prompt exactly once.
// The control plane RE-PUSHES every still-pending event on each stream reconnect (so a daemon that
// missed one still gets it); a flapping stream therefore delivers the same pending launch many times.
// Without this guard the console re-printed the prompt on every reconnect — a handful of un-actioned
// requests turned into tens of thousands of prompt lines. Returns false for a reconnect re-push.
func announcePendingLaunch(mu *sync.Mutex, pending map[string]pendingLaunch, ev api.LaunchEvent) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, seen := pending[ev.RequestID]; seen {
		return false
	}
	pending[ev.RequestID] = pendingLaunch{label: ev.ProjectLabel, preset: ev.Preset, party: ev.PartyID}
	return true
}

// announcePendingRun is the run-profile (O.2) counterpart of announcePendingLaunch: it records a run
// awaiting the owner's approve-run and reports whether to print the prompt (first sighting only). It
// is silent for a reconnect re-push (already pending) AND for a run the queue already took (already
// in startedRuns) — mirroring the dedup the accepted-run path (enqueueRun) already has.
func announcePendingRun(mu *sync.Mutex, pendingRuns map[string]api.RunEvent, startedRuns map[string]bool, ev api.RunEvent) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, seen := pendingRuns[ev.RunID]; seen || startedRuns[ev.RunID] {
		return false
	}
	pendingRuns[ev.RunID] = ev
	return true
}

// daemonApproveRun is the owner's "yes" for a queued run-profile (O.2). Unlike a launch it
// needs no server round-trip (the RunEvent already carries the full reference), so approval
// runs executeRun directly — resolve against the local registry → drive crank. Clears the
// local pending entry either way.
func daemonApproveRun(d daemonDevice, mu *sync.Mutex, pendingRuns map[string]api.RunEvent, id string, enqueue func(api.RunEvent)) {
	mu.Lock()
	ev, ok := pendingRuns[id]
	mu.Unlock()
	if !ok {
		fmt.Printf("no pending run %q (try `list`)\n", id)
		return
	}
	// Hand it to the serial run-queue (concurrency 1) — same worker as web Start / Auto, so a
	// console approve can't race a web-Started run into a second concurrent crank. enqueue does the
	// dedup + pendingRuns delete under the lock.
	enqueue(ev)
	fmt.Printf("✓ approved run %s — queued %q (%d task(s))…\n", id, ev.ProjectLabel, len(ev.Tasks))
}

func daemonDenyRun(d daemonDevice, mu *sync.Mutex, pendingRuns map[string]api.RunEvent, id string) {
	mu.Lock()
	_, ok := pendingRuns[id]
	delete(pendingRuns, id)
	mu.Unlock()
	if !ok {
		fmt.Printf("no pending run %q\n", id)
		return
	}
	if err := api.SetRunStatus(d.Base, d.Token, id, "declined", "declined by owner"); err != nil {
		fmt.Printf("deny-run failed (%v)\n", err)
		return
	}
	fmt.Printf("✓ declined run %s\n", id)
}

// daemonApproveConsult is the owner's "yes" for a consult: answer it read-only on our own checkout
// (answerConsult owns the resolve → one-shot → post-back lifecycle). Takes the entry out of the
// durable pending set, so the console and the `ctrl-\ p` modal can't both spawn a turn for one id.
func daemonApproveConsult(d daemonDevice, consults *consultQueue, id string) {
	ev, ok := consults.Take(id)
	if !ok {
		fmt.Printf("no pending consult %q (try `list`)\n", id)
		return
	}
	fmt.Printf("✓ answering consult %s read-only…\n", id)
	go answerConsult(d, ev)
}

// daemonDenyConsult is the owner's "no": tell the control plane so the asker is freed at once
// (rather than waiting out the server-side timeout). Clears the pending entry either way.
func daemonDenyConsult(d daemonDevice, consults *consultQueue, id string) {
	if _, ok := consults.Take(id); !ok {
		fmt.Printf("no pending consult %q\n", id)
		return
	}
	if err := api.DeclineConsult(d.Base, d.Token, id, "declined by owner"); err != nil {
		fmt.Printf("deny-consult failed (%v)\n", err)
		return
	}
	fmt.Printf("✓ declined consult %s\n", id)
}

// mirrorProjects pushes the current registry's LABELS (+ presets, never paths) to the
// control plane so teammates' pickers can see them. Best-effort; the local registry is the
// source of truth.
func mirrorProjects(d daemonDevice) error {
	reg := loadDaemonRegistry()
	refs := make([]api.DaemonProjectRef, 0, len(reg.Projects))
	for _, p := range reg.Projects {
		// Engine rides the mirror (Epic #73) so web pickers can show what this machine actually
		// runs per project. Only registry-valid names go up; a hand-edited unknown stays local.
		engine := ""
		if validEngine(p.Engine) {
			engine = p.Engine
		}
		refs = append(refs, api.DaemonProjectRef{Label: p.Label, Preset: p.Preset, Engine: engine})
	}
	return api.MirrorProjects(d.Base, d.Token, refs)
}

// daemonConfigSnapshot builds the METADATA-ONLY heartbeat payload (#267): CLI version, OS, and the
// advertised projects with the dir BASENAME only. It reads NO files and emits NO absolute path, so
// no secret can leak through the heartbeat — the security invariant of the fleet map.
func daemonConfigSnapshot() api.DaemonConfig {
	cfg := configSnapshotFrom(loadDaemonRegistry().Projects, version, runtime.GOOS)
	cfg.Provision = provisionEnabled()   // P2: report clone-on-demand opt-in so the web only offers it to enrolled nodes
	cfg.AutoUpdate = autoUpdateEnabled() // so the fleet map can show which nodes keep themselves current
	cfg.EngineAccount = engineAccount()  // the Anthropic identity crank spends — surfaced so a wrong-account run isn't a mystery
	cfg.MCPCatalog = mcpCatalogNames()   // #574: catalog NAMES only (metadata, never commands/env) → the web Agent-tools picker
	cfg.LocalRepos = cachedLocalRepos()  // Repository access → "Local directory": handles + display names, never paths
	// Where this machine would PUT a project it doesn't have yet — a disjoint handle namespace from
	// LocalRepos (see api.DaemonConfig), display metadata only, still never a path.
	cfg.Destinations = cachedDestinations()
	// Machine health as of this beat (fleet node health). Best-effort by construction: anything
	// unreadable is an ABSENT key, and nothing in here can fail or delay the beat — see
	// nodemetrics.go. Like local_repos above, it only exists because it is added HERE.
	cfg.Metrics = collectNodeMetrics()
	return cfg
}

// mcpCatalogNames lists the machine's local MCP catalog by NAME only — the grant-picker inventory
// for the web Agent-tools panel (#574). Deliberately metadata-not-values: commands, args, env, and
// any keys stay in ~/.partyline/mcp.json on this machine; a grant later travels back as a name the
// daemon resolves locally. Sorted for a stable heartbeat payload.
func mcpCatalogNames() []string {
	cat := loadMCPCatalog()
	if len(cat) == 0 {
		return nil
	}
	names := make([]string, 0, len(cat))
	for n := range cat {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// engineAccount reads the machine's `claude` login identity ("email · org") from ~/.claude.json,
// best-effort. Identity ONLY — the file also holds tokens; we read exactly the two display fields
// and never forward anything else. Empty on any error (not logged in, unreadable, older CLI), so a
// failure just means the fleet map omits the account rather than breaking the heartbeat.
func engineAccount() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return ""
	}
	var d struct {
		OAuthAccount struct {
			Email string `json:"emailAddress"`
			Org   string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	email, org := strings.TrimSpace(d.OAuthAccount.Email), strings.TrimSpace(d.OAuthAccount.Org)
	switch {
	case email != "" && org != "":
		return email + " · " + org
	case email != "":
		return email
	default:
		return ""
	}
}

// namedCheckNames reads a project's .partyline/verify and returns the NAMES of its named checks.
// Nothing else from the file leaves the machine: the commands stay here, because a repo-authored
// shell command in the control plane is the boundary this design exists to hold.
//
// Best-effort by construction — no file, an unreadable file, or a file of only unnamed commands all
// yield nothing, and the settings page simply has nothing to offer for that project.
func namedCheckNames(dir string) []string {
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, verifyFile))
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range parseChecks(string(b)) {
		if c.Named {
			out = append(out, c.Name)
		}
	}
	if len(out) > 50 {
		out = out[:50] // a bounded list; nobody has fifty acceptance checks
	}
	return out
}

// configSnapshotFrom is the pure transform (extracted so the no-secret-leak invariant is
// unit-testable): every project contributes label/preset/engine + the dir BASENAME only. An
// absolute path in yields a basename out — the path never reaches the payload.
func configSnapshotFrom(projects []daemonProject, ver, goos string) api.DaemonConfig {
	projs := make([]api.DaemonProjectInfo, 0, len(projects))
	for _, p := range projects {
		projs = append(projs, api.DaemonProjectInfo{
			Label:   p.Label,
			Preset:  p.Preset,
			Engine:  p.Engine,
			DirBase: filepath.Base(p.Path),
			Checks:  namedCheckNames(p.Path),
		})
	}
	return api.DaemonConfig{Version: ver, OS: goos, Projects: projs}
}

// syncIfEnrolled mirrors projects after a registry change, but only if a device is enrolled.
func syncIfEnrolled() {
	if d := loadDaemonDevice(); d.Token != "" {
		if err := mirrorProjects(d); err == nil {
			fmt.Println("  (mirrored to your account)")
		}
	}
}

// mirrorIfEnrolled is syncIfEnrolled WITHOUT the printed line, for callers whose stdout is a
// protocol rather than a terminal: cg-mcp speaks JSON-RPC on stdout, so one stray "(mirrored…)"
// is a parse error in the engine, not a friendly note.
func mirrorIfEnrolled() {
	if d := loadDaemonDevice(); d.Token != "" {
		_ = mirrorProjects(d)
	}
}

func daemonRequests() {
	d := loadDaemonDevice()
	if d.Token == "" {
		fatal(fmt.Errorf("daemon not enabled — run `ptln daemon enable` first"))
	}
	reqs, err := api.ListPendingLaunches(d.Base, d.Token)
	if err != nil {
		fatal(err)
	}
	if len(reqs) == 0 {
		fmt.Println("no pending launch requests")
		return
	}
	fmt.Println("pending launch requests:")
	for _, r := range reqs {
		fmt.Printf("  %s  %q (%s)  party %s\n", r.RequestID, r.ProjectLabel, r.Preset, r.PartyID)
	}
	fmt.Println("approve from a running `ptln daemon run` console.")
}

// ---- launched-child PID store (R5 kill switch) ----

// launchRecord ties a launch request to the spawned child's PID, persisted so `daemon kill`
// (a separate process from `run`) — or a fresh `run` after a restart — can still terminate it.
type launchRecord struct {
	PID   int    `json:"pid"`
	Label string `json:"label"`
}

func launchesPath() string { return filepath.Join(daemonDir(), "launches.json") }

func loadLaunchRecords() map[string]launchRecord {
	m := map[string]launchRecord{}
	if b, err := os.ReadFile(launchesPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveLaunchRecords(m map[string]launchRecord) {
	if err := os.MkdirAll(filepath.Dir(launchesPath()), 0o700); err != nil {
		return
	}
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(launchesPath(), b, 0o600)
	}
}

func recordLaunch(reqID string, pid int, label string) {
	m := loadLaunchRecords()
	m[reqID] = launchRecord{PID: pid, Label: label}
	saveLaunchRecords(m)
}

func forgetLaunch(reqID string) {
	m := loadLaunchRecords()
	if _, ok := m[reqID]; ok {
		delete(m, reqID)
		saveLaunchRecords(m)
	}
}

// runProcs tracks the crank child for each in-flight run so a web cancel can actually stop the work.
// The daemon runs one crank at a time (the serial queue), but this is keyed by run id anyway so the
// fire-and-forget path is covered too. Registered right after spawn, cleared when the child exits.
var (
	runProcsMu sync.Mutex
	runProcs   = map[string]int{} // runID → child PID (its own process-group leader via Setsid)
)

func trackRun(runID string, pid int) {
	if runID == "" || pid <= 0 {
		return
	}
	runProcsMu.Lock()
	runProcs[runID] = pid
	m := loadRunRecords()
	m[runID] = pid
	saveRunRecords(m)
	runProcsMu.Unlock()
}

func untrackRun(runID string) {
	runProcsMu.Lock()
	delete(runProcs, runID)
	m := loadRunRecords()
	if _, ok := m[runID]; ok {
		delete(m, runID)
		saveRunRecords(m)
	}
	runProcsMu.Unlock()
}

// ---- in-flight run PID store (crash recovery) ----
//
// The in-memory runProcs map dies with the process, so a machine that goes down mid-crank left
// its run stuck `running` server-side forever — the web's stalled-run CTA was the only way out,
// and only if a human noticed. Mirroring the map to disk (same pattern as the launch-record
// store) lets the NEXT daemon start reconcile honestly: see sweepOrphanRuns.

func runRecordsPath() string { return filepath.Join(daemonDir(), "runs.json") }

func loadRunRecords() map[string]int {
	m := map[string]int{}
	if b, err := os.ReadFile(runRecordsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveRunRecords(m map[string]int) {
	if err := os.MkdirAll(filepath.Dir(runRecordsPath()), 0o700); err != nil {
		return
	}
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(runRecordsPath(), b, 0o600)
	}
}

// sweepOrphanRuns runs once at daemon startup: any recorded in-flight run whose crank process no
// longer exists is ORPHANED — the machine went down mid-run (crank children are detached, so a
// plain daemon restart leaves them alive and self-reporting; only a reboot/kill strands them).
// For each orphan the server still believes is in flight, park the run in needs_approval with an
// honest reason — the branch and any resume handle survived on disk, so a web Continue resumes
// it. Certainty rule: a LIVE pid is left strictly alone (it's either the detached crank or,
// rarely, a recycled pid — acting on a guess could mark a working run dead), and a dead pid only
// parks a run whose server status is still running/accepted (never one that finished and
// reported before the crash).
func sweepOrphanRuns(d daemonDevice) {
	recs := loadRunRecords()
	if len(recs) == 0 {
		return
	}
	alive := func(pid int) bool {
		if pid <= 0 {
			return false
		}
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	kept := map[string]int{}
	for runID, pid := range recs {
		if alive(pid) {
			kept[runID] = pid
			continue
		}
		status, err := api.RunStatus(d.Base, d.Token, runID)
		if err == nil && (status == "running" || status == "accepted") {
			// Evidence-based reconciliation: if every reported task finished clean, the run COMPLETED —
			// its exit watcher was simply orphaned by a daemon restart between spawn and exit (the
			// self-update case). That's a `done`, not a "machine went down". Only genuinely interrupted
			// work (tasks still queued/running, or any failed/blocked) parks for the human.
			if tasks, terr := api.ListRunTasks(d.Base, d.Token, runID); terr == nil && allTasksDone(tasks) {
				_ = api.SetRunStatus(d.Base, d.Token, runID, "done", "")
				fmt.Printf("↻ orphaned run %s finished clean while unwatched (daemon restarted mid-run) — marked done\n", runID)
			} else {
				_ = api.SetRunStatus(d.Base, d.Token, runID, "needs_approval",
					"this machine went down mid-run — the work so far is safe on its branch; Continue resumes it")
				fmt.Printf("↻ orphaned run %s (machine went down mid-run) — parked for review\n", runID)
			}
		}
		// A dead pid means the record is stale regardless of what the server said — drop it.
	}
	runProcsMu.Lock()
	saveRunRecords(kept)
	// Re-adopt the still-alive detached children into the in-memory registry so a pause / resume /
	// kill issued AFTER a daemon restart can still signal them (a plain service restart leaves the
	// detached crank running; before this, the restarted daemon had an empty map and couldn't touch
	// it). Their terminal status is still lost if they exit unobserved — that's the orphan tradeoff —
	// but signalling a paused run to continue or die is exactly what the operator needs post-restart.
	for runID, pid := range kept {
		runProcs[runID] = pid
	}
	runProcsMu.Unlock()
}

// allTasksDone: every reported task reached `done` — the evidence that an unwatched run actually
// completed. Empty/no rows is NOT completion (a crank that died before reporting anything must park
// for the human, never silently pass).
func allTasksDone(tasks []api.RunTaskStatus) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.Status != "done" {
			return false
		}
	}
	return true
}

// killRun SIGTERMs the crank process GROUP for a run (negative pid — startDetached gives the child its
// own group via Setsid, so this also stops the engine/worker subprocesses it spawned). The server has
// already flipped the run to `killed`; this is what makes the work actually STOP instead of running to
// completion with its writes ignored. Returns whether a local process was found.
func killRun(runID string) bool {
	runProcsMu.Lock()
	pid, ok := runProcs[runID]
	runProcsMu.Unlock()
	if !ok || pid <= 0 {
		return false // not ours / already finished — nothing to stop
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	// SIGCONT after the SIGTERM: a PAUSED run's group is SIGSTOP'd, and a stopped process never
	// runs its signal handler (the pending SIGTERM would just sit there and the run would never
	// die). Continuing it lets it wake, see the SIGTERM, and exit. Harmless for a running group.
	_ = syscall.Kill(-pid, syscall.SIGCONT)
	untrackRun(runID)
	return true
}

// pauseRun SIGSTOPs the crank process GROUP for a run (negative pid — startDetached gives the child
// its own group via Setsid, so the engine/worker subprocesses stop too). The server has already
// flipped the run to `paused`; this is what makes the work actually STOP consuming CPU/tokens mid-
// flight (the group freezes in place, no exit, so the daemon's waitRun stays blocked on it). A later
// resumeRun (SIGCONT) continues the SAME process where it left off. Returns whether a local process
// was found. The run stays TRACKED in runProcs — a paused run is still ours to resume or kill.
func pauseRun(runID string) bool {
	runProcsMu.Lock()
	pid, ok := runProcs[runID]
	runProcsMu.Unlock()
	if !ok || pid <= 0 {
		return false // not ours / already finished — nothing to hold
	}
	_ = syscall.Kill(-pid, syscall.SIGSTOP)
	return true
}

// resumeRun SIGCONTs a previously-paused crank process GROUP so it continues from exactly where it
// was held (the run's in-memory + on-disk state survived the freeze). The server has already flipped
// the run back to `running`. Returns whether a local process was found.
func resumeRun(runID string) bool {
	runProcsMu.Lock()
	pid, ok := runProcs[runID]
	runProcsMu.Unlock()
	if !ok || pid <= 0 {
		return false // not ours / already finished — nothing to resume
	}
	_ = syscall.Kill(-pid, syscall.SIGCONT)
	return true
}

// killLaunch SIGTERMs the spawned child's process GROUP (negative pid — the child is its own
// group leader via Setsid, so this also stops any party/MCP subprocess it spawned), marks the
// request `killed` server-side (best-effort), and forgets the local record. Shared by the CLI
// `kill` command and the run loop's web-kill handler. Returns whether a local process existed.
func killLaunch(d daemonDevice, reqID, note string) bool {
	rec, ok := loadLaunchRecords()[reqID]
	if ok && rec.PID > 0 {
		_ = syscall.Kill(-rec.PID, syscall.SIGTERM)
	}
	if d.Token != "" {
		_ = api.SetLaunchStatus(d.Base, d.Token, reqID, "killed", note)
	}
	forgetLaunch(reqID)
	return ok
}

func daemonKill(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln daemon kill <request-id>"))
	}
	reqID := args[0]
	d := loadDaemonDevice()
	if killLaunch(d, reqID, "killed by owner") {
		fmt.Printf("✓ killed %s\n", reqID)
	} else {
		fmt.Printf("marked %s killed (no local process record — already gone?)\n", reqID)
	}
}

// daemonLaunchRequest is the REQUESTER-side dev/test trigger (the R4 web "Add agent" UI does
// this). As the logged-in user, ask a daemon to launch a project into a party.
func daemonLaunchRequest(args []string) {
	fs := flag.NewFlagSet("launch-request", flag.ExitOnError)
	party := fs.String("party", "", "party id")
	daemonID := fs.String("daemon", "", "target daemon id")
	label := fs.String("label", "", "project label")
	preset := fs.String("preset", "spec", "spec | chat")
	_ = fs.Parse(args)
	if *party == "" || *daemonID == "" || *label == "" {
		fatal(fmt.Errorf("usage: ptln daemon launch-request --party <id> --daemon <id> --label <label> [--preset spec|chat]"))
	}
	if api.LoadToken() == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login` first"))
	}
	id, err := api.New().CreateLaunchRequest(*party, *daemonID, *label, *preset)
	if err != nil {
		if isAuthErr(err) {
			fatal(fmt.Errorf("not logged in (or your session expired) — run `ptln login` first"))
		}
		fatal(err)
	}
	fmt.Printf("✓ launch request %s created\n  the target daemon's owner approves it in `ptln daemon run`\n", id)
}

func daemonDisable() {
	d := loadDaemonDevice()
	if d.Token == "" {
		fmt.Println("daemon not enabled — nothing to disable.")
		return
	}
	if err := api.RevokeDaemon(d.Base, d.Token); err != nil {
		fmt.Printf("⚠ server revoke failed (%v) — removing the local credential anyway\n", err)
	}
	if err := os.Remove(daemonDevicePath()); err != nil && !os.IsNotExist(err) {
		fatal(err)
	}
	fmt.Println("✓ daemon disabled (device token revoked, local credential removed)")
}

// ---- the reference → command resolver (the security chokepoint) ----

// launchRef is what the control plane sends: a reference, never a command. (R0 passes the
// party link directly via the local trigger; R1+ obtains it by exchanging a single-use,
// short-TTL join handle so the raw token never travels down the daemon stream.)
type launchRef struct {
	ProjectLabel string
	PartyLink    string
	// Engine (optional) is the project's server-configured planning engine, carried on the
	// accepted event. Reference-not-command: resolveLaunch validates it against the engines
	// registry keys; empty/unknown falls back to the local registry's per-project engine.
	Engine string
	// Grants (optional, #574) are the org's tool grants for this project's planning agents —
	// names/prefixes only. resolveLaunch re-validates and resolves them locally
	// (resolveLaunchGrants); absent = the unchanged read-only posture.
	Grants *api.ToolGrants
}

// resolveLaunch is the ONLY place a reference becomes a runnable command. It exact-matches
// the label against the local registry (so injection can't widen scope), verifies the dir
// still exists, and builds a fixed argv. Returns the argv + working dir, or an error.
// projectByLabel returns the registered project for an EXACT label match, or nil. The exact
// match is the security chokepoint — an injection-y label simply doesn't resolve.
func projectByLabel(reg daemonRegistry, label string) *daemonProject {
	for i := range reg.Projects {
		if reg.Projects[i].Label == label {
			return &reg.Projects[i]
		}
	}
	return nil
}

func resolveLaunch(reg daemonRegistry, ref launchRef) (argv []string, dir string, err error) {
	proj := projectByLabel(reg, ref.ProjectLabel)
	if proj == nil {
		return nil, "", fmt.Errorf("unknown project %q — not in the local registry", ref.ProjectLabel)
	}
	if !strings.HasPrefix(ref.PartyLink, "http") {
		return nil, "", fmt.Errorf("invalid party link")
	}
	if fi, e := os.Stat(proj.Path); e != nil || !fi.IsDir() {
		return nil, "", fmt.Errorf("project dir is gone: %s", proj.Path)
	}
	// Engine: a VALID server-sent planning engine (the accepted event's `engine`) wins over
	// the local registry's per-project engine; empty/unknown keeps today's behavior (local,
	// "" = claude). preferEngine (engine_oneshot.go) is the ONE copy of that pecking order,
	// shared with the describe/review run jobs — an injection-y value never reaches the argv,
	// it just falls back with a notice.
	engine, note := preferEngine(proj.Engine, ref.Engine)
	if note != "" {
		fmt.Fprintf(os.Stderr, "  (launch %s: %s)\n", proj.Label, note)
	}
	argv = []string{"party", ref.PartyLink, "--name", proj.Label}
	if engine != "" && engine != "claude" {
		argv = append(argv, "--engine", engine) // default (empty) stays claude
	}
	// NOTE: grounding (cited-position mode) is NOT decided here. It's a party-MODE property the
	// daemon can't see at launch — party_agent.grounded() turns it on for approach-review parties
	// only. Forcing --evidence for every "spec"-preset project silently grounded describe/chat
	// parties, overriding their facilitation persona (the empty-doc "the agent died" bug).
	// Read-only posture, per engine (R6 — a daemon-launched agent reads the repo and proposes;
	// it never writes). Each tail is that engine's own enforcement, passed through after `--`:
	//   claude  --allowedTools Read,Grep,Glob   (per-tool allowlist; writes denied)
	//   codex   --sandbox read-only             (OS-level sandbox; writes denied)
	//   gemini  --approval-mode plan            (read-only mode; write tools denied)
	// antigravity has no read-only flag, but its headless -p is deny-by-default: a tool call
	// blocks on an approval no one can give and times out — no writes can happen, tools are
	// effectively unavailable. We never pass its only alternative (--dangerously-skip-permissions).
	switch engineLabel(engine) {
	case "claude":
		// #574: the org's tool grants WIDEN the base read-only allowlist — never replace it.
		// Every widening is audited on the daemon console; invalid/unknown entries are skipped.
		allow := []string{"Read", "Grep", "Glob"}
		var grantArgs []string
		if ref.Grants != nil {
			extra, mcpCfg, notes := resolveLaunchGrants(ref.Grants, loadMCPCatalog())
			for _, n := range notes {
				fmt.Fprintf(os.Stderr, "  (launch %s: %s)\n", proj.Label, n)
			}
			if len(extra) > 0 {
				fmt.Fprintf(os.Stderr, "  launch %s: tool grants applied → %s\n", proj.Label, strings.Join(extra, " "))
				allow = append(allow, extra...)
			}
			if mcpCfg != "" {
				grantArgs = append(grantArgs, "--mcp-config", mcpCfg)
			}
		}
		argv = append(argv, "--")
		argv = append(argv, grantArgs...)
		argv = append(argv, "--allowedTools", strings.Join(allow, ","))
	case "codex":
		if ref.Grants != nil {
			fmt.Fprintf(os.Stderr, "  (launch %s: tool grants not yet applied for codex — read-only posture unchanged)\n", proj.Label)
		}
		argv = append(argv, "--", "--sandbox", "read-only")
	case "gemini":
		if ref.Grants != nil {
			fmt.Fprintf(os.Stderr, "  (launch %s: tool grants not yet applied for gemini — read-only posture unchanged)\n", proj.Label)
		}
		argv = append(argv, "--", "--approval-mode", "plan")
	}
	return argv, proj.Path, nil
}

// startDetached runs the resolved command DETACHED (survives the daemon via Setsid), in the
// project dir, logging to ~/.partyline/daemon/launches/<label>.log. Returns the started *Cmd
// so a caller that wants a terminal outcome (executeRun) can Wait on it; spawnLaunch discards
// it (fire-and-forget). The label is an owner-authored registry label (validated by the
// resolver), never a server-supplied string, so it's safe in the log filename.
func startDetached(argv []string, dir, label string, extraEnv []string) (cmd *exec.Cmd, logPath string, err error) {
	logDir := filepath.Join(daemonDir(), "launches")
	if err = os.MkdirAll(logDir, 0o700); err != nil {
		return nil, "", err
	}
	logPath = filepath.Join(logDir, label+".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", err
	}
	cmd = exec.Command(selfExe(), argv...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	// Setsid makes the child its own process-group leader, so a kill can target the whole
	// group (negative pid) and take down party + any MCP subprocess it spawned.
	return cmd, logPath, nil
}

// spawnLaunch runs the resolved command DETACHED and returns the child's pid (fire-and-forget:
// the caller does not Wait). Mirrors the --clone detach pattern.
func spawnLaunch(argv []string, dir, label string) (pid int, logPath string, err error) {
	cmd, logPath, err := startDetached(argv, dir, label, nil)
	if err != nil {
		return 0, "", err
	}
	return cmd.Process.Pid, logPath, nil
}

// daemonLaunchLocal is the R0 spike: feed a reference locally and watch it resolve + spawn —
// proving the core mechanic without any server. (R1/R3 replace the local trigger with the
// authenticated outbound stream + the owner confirm.)
func daemonLaunchLocal(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: ptln daemon launch-local <project-label> '<party-link>'"))
	}
	ref := launchRef{ProjectLabel: args[0], PartyLink: args[1]}
	argv, dir, err := resolveLaunch(loadDaemonRegistry(), ref)
	if err != nil {
		fatal(fmt.Errorf("resolve: %w", err))
	}
	pid, logPath, err := spawnLaunch(argv, dir, ref.ProjectLabel)
	if err != nil {
		fatal(fmt.Errorf("spawn: %w", err))
	}
	fmt.Printf("✓ launched %q in %s (pid %d)\n  argv: ptln %s\n  log:  %s\n", ref.ProjectLabel, dir, pid, strings.Join(argv, " "), logPath)
}
