// Shared CLI helpers used across the shell + account/team commands.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
	eng "partyline.sh/partyline/internal/engine"
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
	fmt.Fprintln(os.Stderr, "ptln:", err)
	os.Exit(1)
}

// hasFlag and clip lived in trigger_cmds.go and outlived it: triggers were the auto-start path and
// went, but half the CLI uses these two.
func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func flagVal(args []string, f string) string {
	for i, a := range args {
		if a == f && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, f+"=") {
			return strings.TrimPrefix(a, f+"=")
		}
	}
	return ""
}

// apiClientOrFatal is "a signed-in client, or stop with the command that fixes it". It lived in
// trigger_cmds.go under the name triggerClient and was never trigger-specific.
func apiClientOrFatal() *api.Client {
	c := api.New()
	if c == nil {
		fatal(fmt.Errorf("not logged in on this machine — run: ptln login"))
	}
	return c
}

// advertises reports whether a machine offers this project label. Named apart from work_test.go's
// hasStr, which is a test helper for a different question.
func advertises(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

// firstWords returns at most n words of s — used for one-line summaries in menus and branch names.
func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// pokeStatus nudges tmux to repaint the status line after something changed underneath it. A no-op
// when this machine has no partyline tmux config, which is the common case outside a session.
func pokeStatus() {
	if _, err := os.Stat(filepath.Join(stateDir(), "tmux.conf")); err != nil {
		return
	}
	_ = tmuxCmd("refresh-client", "-S").Run()
}

// engineForCwd is the engine to use when nothing names one.
//
// It used to consult the daemon's per-project registry, which decided the engine centrally. With
// the control plane gone there is no registry to consult and no server opinion to honour, so this
// is the documented default and the caller's explicit choice overrides it.
func engineForCwd() string { return "claude" }

// engineLabel renders an engine id for humans.
func engineLabel(e string) string { return eng.Label(e) }

// selfExe is this binary's absolute path, used wherever partyline writes a command line that will
// be run later — hook configs above all. os.Args[0] is the fallback and is only reached when the
// executable cannot be resolved at all.
func selfExe() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return os.Args[0]
}

// expandTilde resolves a leading ~ in a user-supplied path.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}

// openBrowser opens a URL in the user's default browser, best-effort and never blocking.
func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	}
}

// plural picks the singular or plural wording for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// clipVis truncates to a VISIBLE width, counting what a terminal renders rather than bytes, so a
// string carrying ANSI colour or wide runes is not cut mid-escape.
func clipVis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return brand.ClipEllipsis(s, w)
}

// oneLine flattens whitespace so a multi-line body renders on one row.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// firstLineOf returns everything before the first newline.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// atoiOr parses an int, falling back to def on anything unparseable.
func atoiOr(s string, def int) int {
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil {
		return def
	}
	return n
}
func tailString(s string, n int) string {
	if s = strings.TrimSpace(s); len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// shellQuote quotes a string for safe inclusion in a shell command line.
func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'$`\\") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
