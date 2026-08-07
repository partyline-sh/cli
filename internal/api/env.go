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

const prodBase = "https://partyline.sh"

// A host is a path segment here, so anything exotic is flattened rather than trusted. Belt and
// braces: PARTYLINE_API is developer-supplied, but a value like "https://x/../.." should not be
// able to walk out of the config directory.
var hostSafe = regexp.MustCompile(`[^a-zA-Z0-9.\-]`)

// IsProd reports whether the CLI is pointed at the production control plane.
func IsProd() bool { return Base() == prodBase }

// EnvLabel is the short name for the current control plane, for display. Empty when production —
// the common case should be unadorned.
func EnvLabel() string {
	if IsProd() {
		return ""
	}
	u, err := url.Parse(Base())
	if err != nil || u.Host == "" {
		return "custom"
	}
	// staging.partyline.sh → staging; localhost:3111 → localhost:3111
	if strings.HasSuffix(u.Host, ".partyline.sh") {
		return strings.TrimSuffix(u.Host, ".partyline.sh")
	}
	return u.Host
}

// ConfigDir is the root for credentials and daemon state for the CURRENT control plane.
// Production → ~/.partyline. Anything else → ~/.partyline/envs/<host>.
func ConfigDir() string {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".partyline")
	if IsProd() {
		return root
	}
	u, err := url.Parse(Base())
	host := ""
	if err == nil {
		host = u.Host
	}
	if host == "" {
		host = "custom"
	}
	return filepath.Join(root, "envs", hostSafe.ReplaceAllString(host, "_"))
}
