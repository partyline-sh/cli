package api

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Per-environment config isolation.
//
// One binary talks to whichever control plane PARTYLINE_API points at, but credentials were stored
// at fixed paths — so pointing ptln at staging OVERWROTE the production token and account, and the
// daemon would re-register the same machine against whichever plane it last saw. Dogfooding on
// staging would quietly cost you your prod login.
//
// Production keeps ~/.partyline/ EXACTLY as before: no migration, no moved files, nothing for an
// existing install to notice. Every other endpoint gets ~/.partyline/envs/<host>/, so several
// environments coexist and switching is just an env var.
//
// Deliberately NOT namespaced: mcp.json, keepgoing, onboarded, tray-quiet, the scan index. Those
// are local preferences about this machine, not credentials for a control plane, and splitting them
// would mean re-onboarding every time you switched environment.

// prodBase was the hosted control plane. IT NO LONGER SERVES AN API.
//
// partyline.sh is documentation and marketing now; the Hetzner boxes behind the app are shut down.
// The constant survives for exactly two jobs, both about NOT breaking existing installs:
//
//  1. ConfigDir() keeps ~/.partyline/ for it, so a machine that logged in when there WAS a hosted
//     service still finds its own files rather than having them silently move.
//  2. Unconfigured() recognises it, so the CLI can say what actually happened instead of failing
//     against a 404.
//
// It is NOT a working default any more, and nothing should treat it as one.
const prodBase = "https://partyline.sh"

// Unconfigured reports that this CLI has no control plane it can actually talk to: either nothing
// is set, or it is still pointed at partyline.sh, which serves no API.
//
// Every command that needs a control plane should check this and print NoInstanceNotice, because
// the alternative — dialling partyline.sh and reporting whatever HTML a documentation site returns
// — tells the user nothing about what to do.
func Unconfigured() bool {
	base := Base()
	return base == "" || base == prodBase
}

// NoInstanceNotice is the one message for "you have not told me which partyline to talk to".
//
// It names both ways forward, because the two audiences are different: someone joining a team
// already has an instance and needs its URL, and someone starting out has to run one. Neither is
// obvious from an error that just says a request failed.
func NoInstanceNotice() string {
	return "no partyline instance is configured.\n" +
		"  partyline.sh is documentation — the application is one you run yourself.\n\n" +
		"  Point this machine at an existing instance:\n" +
		"      ptln login https://partyline.example.com\n\n" +
		"  Or install one on this machine:\n" +
		"      ptln server install --site https://partyline.example.com\n\n" +
		"  Docs: https://partyline.sh/docs/self-host"
}

// instancePath is where this machine remembers WHICH partyline it belongs to.
//
// It lives at the ROOT of ~/.partyline, deliberately outside ConfigDir(): ConfigDir is derived from
// the endpoint, so the file that names the endpoint cannot live inside it.
func instancePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "instance")
}

// SaveInstance records the control plane this machine is signed in to, so every later command
// reaches it without an env var.
//
// Written by login. Before this, Base() was "PARTYLINE_API or production", which meant a machine
// signed in to a self-hosted instance still sent every bare `ptln` command to partyline.sh — a
// service that no longer answers. The operator had to prefix PARTYLINE_API on every invocation,
// and the daemon (which had it set) and the interactive CLI (which did not) disagreed about which
// partyline this machine belonged to. That disagreement is also what put two icons in the menu bar:
// the tray bundle is named per endpoint, so the two halves each launched — and each reaped — a
// different one.
func SaveInstance(base string) error {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(instancePath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(instancePath(), []byte(base+"\n"), 0o600)
}

// ForgetInstance clears the remembered endpoint (logout).
func ForgetInstance() { _ = os.Remove(instancePath()) }

// LoadInstance returns the remembered endpoint, or "".
func LoadInstance() string {
	b, err := os.ReadFile(instancePath())
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(string(b)), "/")
}

// A host is a path segment here, so anything exotic is flattened rather than trusted. Belt and
// braces: PARTYLINE_API is developer-supplied, but a value like "https://x/../.." should not be
// able to walk out of the config directory.
var hostSafe = regexp.MustCompile(`[^a-zA-Z0-9.\-]`)

// IsProd reports whether the CLI is pointed at the production control plane.
func IsProd() bool { return Base() == prodBase }

// IsSelfHosted reports whether this CLI is pointed at a control plane that is NOT ours: the base
// URL is configured (PARTYLINE_API) and resolves to something other than partyline.sh.
//
// It exists so the CLI can stay silent toward us when it belongs to someone else's deployment.
// Anything that reports to partyline.sh purely because it is us — anonymous usage telemetry and the
// release/version check — is OFF when this is true, without the operator having to know about
// DO_NOT_TRACK or PARTYLINE_TELEMETRY. Self-hosting is not a preference to be overridden: it means
// the run belongs to a different operator, and their usage is not ours to count.
//
// Deliberately literal about "not partyline.sh": staging.partyline.sh and localhost count as
// self-hosted here too. That errs toward sending nothing, which is the safe direction, and both
// were already excluded in practice (dev builds and CI are skipped anyway).
func IsSelfHosted() bool {
	base := Base()
	return base != "" && base != prodBase
}

// EnvLabel is the short name for the current control plane, for display. Empty when production —
// the common case should be unadorned.
func EnvLabel() string { return EnvLabelFor(Base()) }

// EnvLabelFor is EnvLabel for an arbitrary endpoint.
//
// Split out for the orphan sweep, which has to reconstruct the service name an instance's PREVIOUS
// address was installed under in order to remove it. Deriving that by hand at the call site would
// be a second copy of this rule, and the two would drift.
func EnvLabelFor(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == prodBase {
		return ""
	}
	host := ""
	if u, err := url.Parse(base); err == nil && u != nil {
		host = u.Host
	}
	if host == "" {
		// The sweep passes BARE HOSTS — that is what the registry stores — and url.Parse reads
		// "192.168.1.170:8443" as scheme+opaque, yielding no Host at all. Falling back to the
		// raw value is what makes a stored host and a full URL name the same environment.
		h := strings.TrimSpace(base)
		if h == "" || strings.Contains(h, "/") {
			return "custom"
		}
		host = h
	}
	// staging.partyline.sh → staging; localhost:3111 → localhost:3111
	if strings.HasSuffix(host, ".partyline.sh") {
		return strings.TrimSuffix(host, ".partyline.sh")
	}
	return host
}

// ConfigDir is the root for credentials and daemon state for the CURRENT control plane.
// Production → ~/.partyline. Anything else → ~/.partyline/envs/<dir>.
//
// <dir> is resolved through the instance registry when this machine has confirmed an identity for
// the current host, and falls back to the hostname otherwise. That fallback IS the old behaviour,
// so a machine that has never probed — or is talking to an instance too old to answer — keeps
// finding exactly the files it always did.
//
// The indirection is what lets an instance change address without orphaning its fleet: the
// registry maps the new host to the same directory, so the token and the device enrolment are
// still there. See instance_registry.go for why identity cannot be derived from the URL.
//
// DELIBERATELY NEVER DIALS. This is on the path of essentially every command; resolution has to be
// a local file read. The registry holds the last probe's answer, and login/daemon refresh it.
func ConfigDir() string {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".partyline")
	if IsProd() {
		return root
	}
	base := Base()
	dir := InstanceDirFor(base)
	if dir == "" {
		u, err := url.Parse(base)
		host := ""
		if err == nil {
			host = u.Host
		}
		if host == "" {
			host = "custom"
		}
		dir = host
	}
	// Sanitised on the way out regardless of source: the registry is a file an operator can edit,
	// and a `dir` of "../.." must not walk out of the config tree.
	return filepath.Join(root, "envs", hostSafe.ReplaceAllString(dir, "_"))
}
