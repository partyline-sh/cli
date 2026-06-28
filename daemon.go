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
	"strings"
	"sync"
	"syscall"
	"time"

	"partyline.sh/partyline/internal/api"
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
machine — only project LABELS mirror to the web (later slices) for an "Add agent" picker.
Auto-start as an OS service (launchd/systemd) is a later slice; for now run it yourself.`)
}

// ---- local registry (owner-authored; absolute paths NEVER leave the machine) ----

type daemonProject struct {
	Label  string `json:"label"`
	Path   string `json:"path"`             // absolute dir; local only
	Preset string `json:"preset"`           // "spec" | "chat" — the launch flavor
	Policy string `json:"policy,omitempty"` // "ask" (approve each launch) | "auto" (S3 wires the server side); "" == ask
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

func daemonAddProject(args []string) {
	fs := flag.NewFlagSet("add-project", flag.ExitOnError)
	preset := fs.String("preset", "spec", "launch flavor: spec | chat")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal(fmt.Errorf("usage: ptln daemon add-project <label> [dir] [--preset spec|chat]"))
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
			reg.Projects[i].Path, reg.Projects[i].Preset = abs, *preset
			if err := saveDaemonRegistry(reg); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ updated project %q → %s (%s)\n", label, abs, *preset)
			syncIfEnrolled()
			return
		}
	}
	reg.Projects = append(reg.Projects, daemonProject{Label: label, Path: abs, Preset: *preset})
	if err := saveDaemonRegistry(reg); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ registered project %q → %s (%s)\n", label, abs, *preset)
	syncIfEnrolled()
}

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

func daemonStatus() {
	if d := loadDaemonDevice(); d.Token != "" {
		fmt.Printf("device:   enabled (id %s) → %s\n", d.DaemonID, d.Base)
	} else {
		fmt.Println("device:   not enabled — `ptln daemon enable`")
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
	argv, dir, err := resolveLaunch(reg, launchRef{ProjectLabel: ev.ProjectLabel, PartyLink: link})
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Advertise current projects so a teammate's "Add agent" picker is up to date (R2).
	if err := mirrorProjects(d); err != nil {
		fmt.Printf("⚠ couldn't mirror projects (%v)\n", err)
	}

	var mu sync.Mutex
	pending := map[string]pendingLaunch{}

	fmt.Printf("daemon running (device %s) → %s\n", d.DaemonID, d.Base)
	fmt.Println("  console:  approve <id>  ·  deny <id>  ·  kill <id>  ·  list  ·  ctrl-c to stop")
	go daemonConsole(d, &mu, pending)

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
			func(reqID string) { // web kill — SIGTERM the child + record it
				killLaunch(d, reqID, "killed from web")
				clearPending(&mu, pending, reqID)
				fmt.Printf("\n🛑 stopped %s (kill requested)\n> ", reqID)
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
func daemonConsole(d daemonDevice, mu *sync.Mutex, pending map[string]pendingLaunch) {
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
			if len(pending) == 0 {
				fmt.Println("(no pending requests)")
			}
			for id, p := range pending {
				fmt.Printf("  %s  %q (%s)\n", id, p.label, p.preset)
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
			fmt.Println("commands: approve <id> · deny <id> · kill <id> · list · quit")
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

// mirrorProjects pushes the current registry's LABELS (+ presets, never paths) to the
// control plane so teammates' pickers can see them. Best-effort; the local registry is the
// source of truth.
func mirrorProjects(d daemonDevice) error {
	reg := loadDaemonRegistry()
	refs := make([]api.DaemonProjectRef, 0, len(reg.Projects))
	for _, p := range reg.Projects {
		refs = append(refs, api.DaemonProjectRef{Label: p.Label, Preset: p.Preset})
	}
	return api.MirrorProjects(d.Base, d.Token, refs)
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
	argv = []string{"party", ref.PartyLink, "--name", proj.Label}
	if proj.Preset == "spec" {
		argv = append(argv, "--evidence") // grounded, cited positions (the --spec preset, once built, supersedes this)
	}
	// read-only tools so the grounded agent can cite from the repo without write capability
	argv = append(argv, "--", "--allowedTools", "Read,Grep,Glob")
	return argv, proj.Path, nil
}

// spawnLaunch runs the resolved command DETACHED (survives the daemon), in the project dir,
// logging to ~/.partyline/daemon/launches/<label>.log. Mirrors the --clone detach pattern.
func spawnLaunch(argv []string, dir, label string) (pid int, logPath string, err error) {
	logDir := filepath.Join(stateDir(), "daemon", "launches")
	if err = os.MkdirAll(logDir, 0o700); err != nil {
		return 0, "", err
	}
	logPath = filepath.Join(logDir, label+".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", err
	}
	cmd := exec.Command(selfExe(), argv...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	// Setsid makes the child its own process-group leader, so a kill can target the whole
	// group (negative pid) and take down party + any MCP subprocess it spawned.
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
