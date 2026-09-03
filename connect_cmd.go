package main

import (
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// join_cmd.go — `ptln connect` and `ptln invite-machine`: adding a machine in one paste.
//
// THE FRICTION THIS REMOVES. Every machine had to complete the device flow on its own: run a
// command, read a code, open a browser, approve. That is right for the FIRST machine, where a human
// is proving who they are. It is pure tax on the fifth, where the same human proves the same thing
// again — and it is a dead end on a box with no browser and no second screen, which describes most
// servers.
//
// So the proof is separated from its use. `ptln invite-machine` is run ONCE, somewhere already
// signed in, and prints a command. That command is pasted on as many machines as are being set up,
// each of which enrols, installs its always-on service, and is done — no browser, no device code.
//
// WHY THIS IS AN ACCEPTABLE TRADE. The enrolment token is not a session: it is accepted at exactly
// one endpoint and buys exactly one thing, a device token. It cannot read a board, list projects or
// launch anything, and it expires. The realistic cost of losing one is a stranger's machine
// appearing in the fleet list, where it is visible and revocable — not account access. This is the
// same bargain Tailscale's auth keys make, for the same reason.

func connectUsage() {
	fmt.Println(`Usage: ptln connect <url> <enrolment-token>      connect this machine — no browser needed
       ptln connect <enrolment-token>            (when the instance is already known)

Enrols this machine as a worker and starts its always-on service. Get a token with
` + "`ptln invite-machine`" + ` on a machine that is already signed in.

  --label <name>    what to call this machine in the fleet (default: its hostname)
  --no-service      enrol only; do not install the always-on service`)
}

func connectMachineMain(args []string) {
	label, instance, token := "", "", ""
	noService := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "help":
			connectUsage()
			return
		case a == "--no-service":
			noService = true
		case a == "--label":
			if i+1 >= len(args) {
				fatal(fmt.Errorf("ptln connect: --label needs a value"))
			}
			i++
			label = strings.TrimSpace(args[i])
		case strings.HasPrefix(a, "--label="):
			label = strings.TrimSpace(strings.TrimPrefix(a, "--label="))
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("ptln connect: unknown flag %q", a))
		case strings.HasPrefix(a, api.EnrollmentPrefix):
			token = strings.TrimSpace(a)
		case instance == "":
			instance = strings.TrimRight(strings.TrimSpace(a), "/")
		default:
			fatal(fmt.Errorf("ptln connect: unexpected argument %q", a))
		}
	}
	if token == "" {
		connectUsage()
		fatal(fmt.Errorf("ptln connect: no enrolment token given (they start with %s)", api.EnrollmentPrefix))
	}
	// The URL is optional only when this machine already knows which partyline it belongs to.
	// Guessing would be worse than asking: enrolling against the wrong instance is silent.
	if instance == "" {
		instance = api.LoadInstance()
		if instance == "" || api.Unconfigured() {
			fatal(fmt.Errorf("ptln connect: name the instance too — `ptln connect https://partyline.example.com %s…`",
				api.EnrollmentPrefix))
		}
	}
	if err := validInstanceURL(instance); err != nil {
		fatal(fmt.Errorf("ptln connect %s: %w", instance, err))
	}
	if err := os.Setenv("PARTYLINE_API", instance); err != nil {
		fatal(err)
	}
	fmt.Printf("☎ joining %s\n", instance)

	// Identity BEFORE credentials, exactly as login does: config is keyed by instance, so this is
	// what decides where the device token is written — and lets a machine rejoining an instance
	// that has moved land on the enrolment it already had.
	adoptInstanceIdentity(instance)

	if label == "" {
		label = defaultDeviceLabel()
	}
	c := api.New()
	id, devTok, err := c.RegisterDaemonWithEnrollment(label, token)
	if err != nil && api.IsUnknownAuthority(err) {
		// A self-hosted instance on its own CA — the self-host default. Same TOFU prompt login
		// uses: show the fingerprint, pin on an explicit yes, verify against the pin afterwards.
		if trustInstanceCert(instance) {
			adoptInstanceIdentity(instance) // now reachable; the earlier probe failed on the cert
			id, devTok, err = api.New().RegisterDaemonWithEnrollment(label, token)
		}
	}
	if err != nil {
		// The three ways this legitimately fails are all about the token, so say so rather than
		// leaving someone to guess whether it is the network.
		fatal(fmt.Errorf("ptln connect: %w\n  the token may be expired, revoked, or out of uses — mint a fresh one with `ptln invite-machine`", err))
	}
	if err := saveDaemonDevice(daemonDevice{DaemonID: id, Token: devTok, Base: instance}); err != nil {
		fatal(err)
	}
	if err := api.SaveInstance(instance); err != nil {
		fmt.Fprintln(os.Stderr, "note: enrolled, but could not record the instance: "+err.Error())
	}
	fmt.Printf("  ✓ enrolled as %q (device %s)\n", label, id)

	if stale := reconcileStaleServices(); len(stale) > 0 {
		fmt.Printf("  ☎ removed service(s) from this instance's previous address: %s\n", strings.Join(stale, ", "))
	}
	if noService {
		fmt.Println("  · always-on: skipped (--no-service) — later: `ptln daemon install`")
		return
	}
	// ENROLLING IS NOT RUNNING — the gap that leaves a machine registered, idle and showing as
	// offline. join does both or it has not done its job.
	if serviceInstalled() {
		if err := restartService(); err != nil {
			fmt.Printf("  ✗ could not restart the always-on service (%v) — run `ptln daemon restart`\n", err)
			return
		}
		fmt.Println("  ✓ always-on service restarted on the new credential")
		return
	}
	note, err := installService()
	if err != nil {
		fmt.Printf("  ✗ always-on install failed (%v) — run `ptln daemon install`\n", err)
		return
	}
	fmt.Println("  ✓ " + note)
	fmt.Println("\n  this machine is connected — it appears in the fleet within a minute.")
}

// inviteMachineMain mints a token and prints the command to paste on the next machine.
func inviteMachineMain(args []string) {
	label, ttl, uses := "", 60, 10
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "help":
			fmt.Println(`Usage: ptln invite-machine [--label <name>] [--minutes N] [--uses N]

Prints a command to paste on machines you want to connect. No browser needed there.

  --label <name>   what to call this batch, for the revoke list (default: "enrolment token")
  --minutes N      how long it stays valid (default 60, max 10080)
  --uses N         how many machines may use it (default 10, 0 = unlimited until it expires)`)
			return
		case a == "--label":
			i++
			if i < len(args) {
				label = args[i]
			}
		case a == "--minutes":
			i++
			if i < len(args) {
				ttl = atoiOr(args[i], ttl)
			}
		case a == "--uses":
			i++
			if i < len(args) {
				uses = atoiOr(args[i], uses)
			}
		default:
			fatal(fmt.Errorf("ptln invite-machine: unexpected argument %q", a))
		}
	}
	if api.Unconfigured() {
		fatal(fmt.Errorf("ptln invite-machine: %s", api.NoInstanceNotice()))
	}
	if api.LoadToken() == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login` first"))
	}
	tok, expires, err := api.New().MintEnrollmentToken(label, ttl, uses)
	if err != nil {
		fatal(fmt.Errorf("ptln invite-machine: %w", err))
	}
	limit := fmt.Sprintf("%d machine(s)", uses)
	if uses == 0 {
		limit = "any number of machines"
	}
	fmt.Printf("\n☎ paste this on each machine you want to connect:\n\n")
	fmt.Printf("    ptln connect %s %s\n\n", api.Base(), tok)
	fmt.Printf("  good for %s until %s.\n", limit, expires)
	fmt.Printf("  it enrols a machine and nothing else — it cannot read or change anything.\n")
	fmt.Printf("  revoke it early from Settings, or let it expire.\n")
}

func atoiOr(s string, def int) int {
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil {
		return def
	}
	return n
}
