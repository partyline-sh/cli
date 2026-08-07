// Shared CLI helpers used across the shell + account/team commands.
package main

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/obs"
)

// sanitizeTerminal returns the local terminal to a sane state after mirroring a
// full-screen TUI: disable mouse reporting (1000/1002/1003 + the 1005/1006/1015
// encodings), disable bracketed paste (2004), leave the alternate screen (1049),
// show the cursor (25), re-enable autowrap (7), and reset SGR. A clean session close
// gets these via the host program's own mirrored reset; a dropped/abnormal session
// does NOT, which otherwise strands the shell in mouse mode (stray "command not
// found: 33M…" + bell on every mouse move). No-op when stdout isn't a terminal.
func sanitizeTerminal() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	os.Stdout.WriteString(
		"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
			"\x1b[?2004l\x1b[?1049l\x1b[?25h\x1b[?7h\x1b[0m")
}

// resolveHostName picks the name shown for you in a session. Priority: an explicit
// --name, then your profile's display_name (so your CLI identity matches the web +
// what teammates see), then your OS username (offline / not logged in). We never fall
// back to the raw `handle` — that's a unique id (e.g. "darcy-f1da3c"), not a name.
// The profile lookup is bounded so a flaky network never delays session start.
func resolveHostName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if api.LoadToken() != "" {
		ch := make(chan string, 1)
		go func() {
			if p, err := api.New().Me(); err == nil {
				ch <- p.DisplayName
			} else {
				ch <- ""
			}
		}()
		select {
		case n := <-ch:
			if strings.TrimSpace(n) != "" {
				return n
			}
		case <-time.After(2500 * time.Millisecond):
		}
	}
	return defaultName()
}

func defaultName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return "host"
}

// stateDir is MACHINE-local state: tray preference, version-check cache, workspace layout,
// telemetry install-id, the ssh host key. None of it belongs to a control plane, so it stays at
// ~/.partyline regardless of which one PARTYLINE_API points at — otherwise switching environment
// would silently re-onboard you and mint a new telemetry identity.
func stateDir() string {
	home, _ := os.UserHomeDir()
	d := filepath.Join(home, ".partyline")
	_ = os.MkdirAll(d, 0o700)
	return d
}

// daemonDir is CONTROL-PLANE state: the device token, job records, worklists, injected globals,
// consult budget. A daemon is registered to exactly one control plane, so this lives under the
// per-environment root (api.ConfigDir) — production keeps ~/.partyline/daemon, staging gets its
// own. Without the split, running a staging daemon on a machine would overwrite the prod daemon's
// identity and the fleet would quietly lose a node.
func daemonDir() string {
	d := filepath.Join(api.ConfigDir(), "daemon")
	_ = os.MkdirAll(d, 0o700)
	return d
}

// localIPs lists join targets, tailscale CGNAT range (100.64/10) first.
func localIPs() []string {
	var ts, rest []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		ip := ipn.IP.String()
		if ipn.IP.To4()[0] == 100 && ipn.IP.To4()[1] >= 64 && ipn.IP.To4()[1] < 128 {
			ts = append(ts, ip)
		} else {
			rest = append(rest, ip)
		}
	}
	return append(ts, rest...)
}

func fatal(err error) {
	obs.CaptureError(err)
	obs.Flush() // os.Exit skips the deferred flush in main()
	fmt.Fprintln(os.Stderr, "ptln:", err)
	os.Exit(1)
}
