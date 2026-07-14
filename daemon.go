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

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
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
	case "projects", "status", "ls":
		daemonStatus()
	case "launch-local":
		daemonLaunchLocal(args[1:])
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
                                                    (approve <id> / deny <id> / list) for launch requests
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
                                                    (dir defaults to the current directory)
  remove-project <label>                            unregister
  status                                            show this device + registered projects
  launch-request --party <id> --daemon <id>         (dev/test) ask a daemon to launch a project,
            --label <label> [--preset spec|chat]    as the logged-in user — the web "Add agent" UI in R4
  launch-local <label> '<party-link>'               R0 spike: resolve + spawn locally (no server)

State lives under ~/.partyline/daemon/ (registry.json + device.json) and never leaves this
machine — only project LABELS mirror to the web for an "Add agent" picker. Run it in the
foreground with 'run', or 'install' it as an always-on background service.`)
}

// ---- local registry (owner-authored; absolute paths NEVER leave the machine) ----

type daemonProject struct {
	Label  string `json:"label"`
	Path   string `json:"path"`             // absolute dir; local only
	Preset string `json:"preset"`           // "spec" | "chat" — the launch flavor
	Policy string `json:"policy,omitempty"` // "ask" (approve each launch) | "auto" (S3 wires the server side); "" == ask
	Engine string `json:"engine,omitempty"` // claude|codex|gemini|antigravity; "" == claude (the party default)
}

// launchPolicy normalizes a project's approval policy (empty/unknown → "ask", the safe
// default). "auto" pre-accepts launches (server-side wiring lands in S3); "ask" gates each.
func (p daemonProject) launchPolicy() string {
	if p.Policy == "auto" {
		return "auto"
	}
	return "ask"
}

type daemonRegistry struct {
	Projects []daemonProject `json:"projects"`
}

func daemonRegistryPath() string { return filepath.Join(stateDir(), "daemon", "registry.json") }

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
	path := filepath.Join(stateDir(), "daemon", "run.lock")
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

func daemonDevicePath() string { return filepath.Join(stateDir(), "daemon", "device.json") }

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

func daemonEnable(args []string) {
	if api.LoadToken() == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login` first"))
	}
	if d := loadDaemonDevice(); d.Token != "" {
		fmt.Printf("daemon already enabled (device %s). `ptln daemon run` to start it,\nor `ptln daemon disable` first to re-enrol.\n", d.DaemonID)
		return
	}
	label := "device"
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		label = strings.TrimSpace(h)
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		label = strings.TrimSpace(args[0])
	}
	id, token, err := api.New().RegisterDaemon(label)
	if err != nil {
		if isAuthErr(err) {
			fatal(fmt.Errorf("not logged in (or your session expired) — run `ptln login` first"))
		}
		fatal(fmt.Errorf("register: %w", err))
	}
	if err := saveDaemonDevice(daemonDevice{DaemonID: id, Token: token, Base: api.Base()}); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ daemon enabled as %q (device %s)\n", label, id)
	fmt.Println("  start it with:  ptln daemon run")
	fmt.Println("  (R1: it connects and listens only — it does not launch anything yet)")
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
func isAutoProject(label string) bool {
	p := projectByLabel(loadDaemonRegistry(), label)
	return p != nil && p.launchPolicy() == "auto"
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
	argv, dir, err := resolveLaunch(reg, launchRef{ProjectLabel: ev.ProjectLabel, PartyLink: link, Engine: ev.Engine})
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
	return runRef{ProjectLabel: ev.ProjectLabel, ThreadID: ev.ThreadID, Tasks: ev.Tasks, Preset: ev.Preset}
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
	// #77 slice 3: per-run merge policy. Only pr/auto are passed (manual is crank's no-op default),
	// and only the two known values — never arbitrary server text into the argv.
	if ev.MergePolicy == "pr" || ev.MergePolicy == "auto" {
		argv = append(argv, "--merge-policy", ev.MergePolicy)
	}
	// Model selection: forward the run's build model to crank. STRICTLY validated (a plain model token —
	// alnum start, then alnum/dot/dash/underscore) so nothing server-supplied can smuggle a flag, path,
	// whitespace, or newline into the argv. An empty/invalid value is simply omitted → engine default.
	if modelRe.MatchString(ev.Model) {
		argv = append(argv, "--model", ev.Model)
	}
	// Engine (Epic #73): forward the run's build engine to crank. Registry-keys-only (validEngine),
	// so nothing injection-shaped reaches the argv; claude/empty is omitted (crank's default).
	if validEngine(ev.Engine) && engineLabel(ev.Engine) != "claude" {
		argv = append(argv, "--engine", ev.Engine)
	}
	return argv, nil
}

// modelRe bounds a model token to a safe shape for the argv: must start alphanumeric (so it can never
// look like a flag), then alnum / dot / dash / underscore, up to 60 chars. Covers claude aliases
// (opus/sonnet/haiku/fable), dated ids (claude-opus-4-8), and other engines' names; rejects anything
// with a slash, space, or leading dash.
var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$`)

// seedRunTasks writes the run's worklist to the per-task store as `queued` (#77 slice 2) BEFORE
// launching crank in claim mode — the workers claim these rows atomically (slice 1). idx is the
// task's position in ev.Tasks, so the idx a worker later reports done/failed against lines up.
// Idempotent (upsert by run_id,idx) and best-effort: a failed seed logs and continues (a task that
// never seeds simply never gets claimed). Tasks are DATA (ev.Tasks), never argv.
func seedRunTasks(d daemonDevice, ev api.RunEvent) {
	for i, task := range ev.Tasks {
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
	if err != nil {
		return nil, fail("resolve", err) // unknown label / bad thread id / no tasks — refused, no spawn
	}
	// Run-owned scalars (--run, --max-tokens) are appended AFTER resolveRun — never through it,
	// so the resolver stays the pure label→path chokepoint. A bad run id refuses the whole run.
	argv, err = augmentRunArgv(argv, ev)
	if err != nil {
		return nil, fail("run id", err)
	}
	// Phase B3: hand crank the project globals (projects.document, carried on the event) as a file it
	// writes into each task's worktree as CLAUDE.md. Best-effort: a write failure logs and runs WITHOUT
	// injected globals rather than failing the run — missing context degrades a run, it doesn't break it.
	// Safe filename: ev.RunID was validated by augmentRunArgv above.
	if strings.TrimSpace(ev.Globals) != "" {
		if gf, gerr := writeRunGlobals(ev.RunID, ev.Globals); gerr == nil {
			argv = append(argv, "--globals-file", gf)
		} else {
			fmt.Fprintf(os.Stderr, "  (run %s: globals write failed, running without project globals: %v)\n", ev.RunID, gerr)
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
	return cmd, nil
}

// waitRun blocks until a spawned crank exits and records the terminal run status.
// budgetPauseExit (3) is crank's "paused, needs approval" signal on an unattended run that hit the
// token ceiling — NOT a failure; verifyPauseExit (Trust · T3) is crank finishing with ≥1 task
// quarantined by a verify gate. Both route to needs_approval so the web surfaces + notifies; any
// other non-zero exit is a failure.
func waitRun(d daemonDevice, ev api.RunEvent, cmd *exec.Cmd) {
	werr := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case errors.As(werr, &ee) && ee.ExitCode() == budgetPauseExit:
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "needs_approval", "token budget reached — approve more or stop")
	case errors.As(werr, &ee) && ee.ExitCode() == verifyPauseExit:
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "needs_approval", "verification failed on one or more tasks — review the quarantined branches, then approve or stop")
	case errors.As(werr, &ee) && ee.ExitCode() == rateLimitExit:
		// Provider rate limit: crank ALREADY self-reported needs_approval with the reset time (which
		// it has and we don't). Leave the status alone — re-writing it here would drop the reset time.
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
		go runDescribeJob(d, ev)
		return nil
	}
	if ev.Preset == "review" {
		go runReviewJob(d, ev)
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
		return runDescribeJob(d, ev)
	}
	if ev.Preset == "review" {
		return runReviewJob(d, ev)
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
	summary, err := runDescribeToWorkItem(client, ev.ThreadID, dir, engineName, ev.Tasks[0], ev.Globals, model, logger.sink(0), 4*time.Minute)
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
// drainRunQueue is the serial run-queue worker (concurrency 1): it takes ONE run off the queue, runs
// it to completion via exec, THEN takes the next — so a batch Started from the web runs one-at-a-time
// instead of spawning five cranks at once. A run failing does NOT stop the queue (chain ordering is
// gated server-side, not here). Extracted from daemonRun so a test can inject a fake exec and assert
// the one-at-a-time guarantee; exec's error is only for the console line, the loop proceeds regardless.
func drainRunQueue(queue <-chan api.RunEvent, exec func(api.RunEvent) error) {
	for ev := range queue {
		if err := exec(ev); err != nil {
			fmt.Printf("\n✗ run %s failed (%v)\n> ", ev.RunID, err)
		}
	}
}

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

	var mu sync.Mutex
	pending := map[string]pendingLaunch{}
	pendingRuns := map[string]api.RunEvent{} // O.2: queued runs awaiting the owner's approve-run
	startedRuns := map[string]bool{}         // run ids already handed to the run-queue — dedup so a
	// stream reconnect that re-pushes the same accepted run (before spawnRun flips it to `running`
	// server-side) can't double-enqueue. Bounded by the process lifetime; a restart re-reads status
	// from the server, where an already-run task is no longer `accepted`.

	// Serial run-queue (concurrency 1). EVERY run this daemon executes without a human waiting at the
	// console — web Start (`go`), Auto projects, AND console approve-run — goes through this one
	// worker, so we never spawn five cranks at once and thrash the machine + API limits when a batch
	// is Started from the web. The worker runs one crank to completion, then takes the next; order is
	// the board order (the stream pushes accepted runs by backlog_rank). A failure / needs_approval
	// does NOT stop the queue — the worker just proceeds to the next run (Slice-2 chains add the only
	// exception: a chained run stays un-eligible until its predecessors are `done`, gated server-side).
	runQueue := make(chan api.RunEvent, 512)
	go drainRunQueue(runQueue, func(ev api.RunEvent) error { return executeRunSync(d, ev) })
	enqueueRun := func(ev api.RunEvent) {
		mu.Lock()
		if startedRuns[ev.RunID] {
			mu.Unlock()
			return
		}
		startedRuns[ev.RunID] = true
		delete(pendingRuns, ev.RunID) // if it was also awaiting console approval, it's the queue's now
		mu.Unlock()
		select {
		case runQueue <- ev:
		default:
			// Queue full (extreme — 512 buffered) — drop the dedup mark so the stream's next re-push
			// of this still-`accepted` run gets another chance rather than being lost.
			mu.Lock()
			delete(startedRuns, ev.RunID)
			mu.Unlock()
		}
	}

	fmt.Printf("daemon running (device %s) → %s\n", d.DaemonID, d.Base)
	fmt.Println("  console:  approve <id>  ·  deny <id>  ·  approve-run <id>  ·  deny-run <id>  ·  kill <id>  ·  list  ·  ctrl-c to stop")
	go daemonConsole(d, &mu, pending, pendingRuns, enqueueRun)

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
				mu.Lock()
				pending[ev.RequestID] = pendingLaunch{label: ev.ProjectLabel, preset: ev.Preset, party: ev.PartyID}
				mu.Unlock()
				fmt.Printf("\n📨 launch request %s — %q (%s)\n   approve %s   ·   deny %s\n> ",
					ev.RequestID, ev.ProjectLabel, ev.Preset, ev.RequestID, ev.RequestID)
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
				if isAutoProject(ev.ProjectLabel) {
					enqueueRun(ev)
					fmt.Printf("\n⚡ queued run %s — %q (Auto, %d task(s))\n> ", ev.RunID, ev.ProjectLabel, len(ev.Tasks))
					return
				}
				mu.Lock()
				pendingRuns[ev.RunID] = ev
				mu.Unlock()
				fmt.Printf("\n📋 run request %s — %q (%s, %d task(s))\n   approve-run %s   ·   deny-run %s\n> ",
					ev.RunID, ev.ProjectLabel, ev.Preset, len(ev.Tasks), ev.RunID, ev.RunID)
			},
			func(reqID string) { // web kill — SIGTERM the child + record it
				killLaunch(d, reqID, "killed from web")
				clearPending(&mu, pending, reqID)
				fmt.Printf("\n🛑 stopped %s (kill requested)\n> ", reqID)
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
			func(ev api.AssignProjectEvent) { // web-assignable projects: bind a candidate the daemon
				// advertised (matched by opaque handle) to a label — reference-not-command intact.
				if err := bindAssignedProject(d, ev); err != nil {
					fmt.Printf("\n⚠ assign project %q failed (%v)\n> ", ev.Label, err)
					return
				}
				fmt.Printf("\n📌 assigned project %q from the web\n> ", ev.Label)
			},
			func(ev api.RelabelEvent) { // projects rename cascade: rename our own registry entry + re-mirror.
				if err := relabelProject(d, ev.OldLabel, ev.NewLabel); err != nil {
					fmt.Printf("\n⚠ rename %q→%q failed (%v)\n> ", ev.OldLabel, ev.NewLabel, err)
					return
				}
				fmt.Printf("\n🏷  renamed project %q → %q from the web\n> ", ev.OldLabel, ev.NewLabel)
			})
		if ctx.Err() != nil {
			fmt.Println("\nstopped.")
			return
		}
		if errors.Is(err, api.ErrDaemonRevoked) {
			fatal(fmt.Errorf("device token revoked — run `ptln daemon enable` again"))
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
func daemonConsole(d daemonDevice, mu *sync.Mutex, pending map[string]pendingLaunch, pendingRuns map[string]api.RunEvent, enqueue func(api.RunEvent)) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			fmt.Print("> ")
			continue
		}
		switch f[0] {
		case "list", "ls":
			mu.Lock()
			if len(pending) == 0 && len(pendingRuns) == 0 {
				fmt.Println("(no pending requests)")
			}
			for id, p := range pending {
				fmt.Printf("  launch %s  %q (%s)\n", id, p.label, p.preset)
			}
			for id, r := range pendingRuns {
				fmt.Printf("  run    %s  %q (%s, %d task(s))\n", id, r.ProjectLabel, r.Preset, len(r.Tasks))
			}
			mu.Unlock()
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
			fmt.Println("commands: approve <id> · deny <id> · approve-run <id> · deny-run <id> · kill <id> · list · quit")
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
	cfg.Candidates = candidateSnapshot() // web-assignable projects: basenames + opaque handles, never a path
	return cfg
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

func launchesPath() string { return filepath.Join(stateDir(), "daemon", "launches.json") }

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
	if proj.Preset == "spec" {
		argv = append(argv, "--evidence") // grounded, cited positions (the --spec preset, once built, supersedes this)
	}
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
		argv = append(argv, "--", "--allowedTools", "Read,Grep,Glob")
	case "codex":
		argv = append(argv, "--", "--sandbox", "read-only")
	case "gemini":
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
	logDir := filepath.Join(stateDir(), "daemon", "launches")
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
