package main

// S4 — Always-on. Installs the daemon as a per-user OS service (launchd on macOS, systemd
// `--user` on Linux) so it stays present + advertising even when the manager TUI is closed,
// and across reboots. The service just runs `ptln daemon run`, which already degrades
// headlessly (no console on a non-tty) — it holds the stream, auto-accepts Auto projects, and
// executes accepted launches; Ask projects are approved from the web modal (no TUI needed).
//
// Install complexity the design flagged: launchd/systemd unit generation + load, and — the
// easy-to-miss one — baking PATH so a spawned agent can still find claude/codex/gemini (a
// service inherits a minimal PATH, not your shell's).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// The always-on unit is PER CONTROL PLANE, for the same reason api.ConfigDir is (see env.go).
// PARTYLINE_API is baked into the unit at install time, and the label/path used to be a fixed
// constant — so `ptln daemon install` while pointed at staging OVERWROTE the production node's
// unit with a staging-pointed one. The machine kept running a daemon, the fleet kept showing it,
// and it was quietly serving the wrong environment.
//
// Production names are UNCHANGED — an existing install has nothing to notice, and no orphaned unit
// is left behind under a new name.
var unitSafe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// envSuffix is "" for production, else a filesystem- and unit-name-safe token for the environment.
func envSuffix() string {
	l := api.EnvLabel()
	if l == "" {
		return "" // production
	}
	// localhost:3111 → localhost-3111. A colon is illegal in a systemd unit name and awkward in a
	// launchd label, and the label doubles as a path segment for the plist.
	s := strings.Trim(unitSafe.ReplaceAllString(l, "-"), "-")
	if s == "" {
		s = "custom"
	}
	return s
}

// serviceLabel is the launchd job label (and the plist filename).
func serviceLabel() string {
	if s := envSuffix(); s != "" {
		return "sh.partyline.daemon." + s
	}
	return "sh.partyline.daemon"
}

// systemdUnitName is the `systemctl --user` unit name, without the .service suffix.
func systemdUnitName() string {
	if s := envSuffix(); s != "" {
		return "partyline-daemon-" + s
	}
	return "partyline-daemon"
}

func serviceLogPath() string { return filepath.Join(daemonDir(), "service.log") }

func launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel()+".plist")
}

func systemdUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName()+".service")
}

// serviceUnitPath is the OS-specific unit file location, or "" on an unsupported platform.
func serviceUnitPath() string {
	switch runtime.GOOS {
	case "darwin":
		return launchdPlistPath()
	case "linux":
		return systemdUnitPath()
	}
	return ""
}

// serviceInstalled reports whether the always-on unit file exists. This is the manager's cue
// to treat presence as Always-on and NOT open a competing stream (the service owns it).
func serviceInstalled() bool {
	p := serviceUnitPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// serviceActive reports whether the OS thinks the service is loaded/running (best-effort —
// used only for `status` output, never for control flow).
func serviceActive() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("launchctl", "list", serviceLabel()).Run() == nil
	case "linux":
		return exec.Command("systemctl", "--user", "is-active", "--quiet", systemdUnitName()).Run() == nil
	}
	return false
}

// restartService restarts the always-on per-user service IN PLACE (no reinstall, no new binary):
// macOS `launchctl kickstart -k` (kill + relaunch the loaded job); Linux `systemctl --user
// restart`. It re-execs the SAME installed binary — so it's the safe local primitive a future
// web-triggered restart would invoke (no code is fetched). Errors if no service is installed.
func restartService() error {
	if !serviceInstalled() {
		return fmt.Errorf("no always-on service installed — run `ptln daemon install` first (or, for a foreground `ptln daemon run`, stop it and re-run)")
	}
	switch runtime.GOOS {
	case "darwin":
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceLabel())
		if err := exec.Command("launchctl", "kickstart", "-k", target).Run(); err != nil {
			return fmt.Errorf("launchctl kickstart %s failed: %w", target, err)
		}
		return nil
	case "linux":
		if err := exec.Command("systemctl", "--user", "restart", systemdUnitName()).Run(); err != nil {
			return fmt.Errorf("systemctl --user restart %s failed: %w", systemdUnitName(), err)
		}
		return nil
	}
	return fmt.Errorf("restart not supported on %s", runtime.GOOS)
}

// stopService stops the always-on service WITHOUT uninstalling it — the "pause this machine"
// action, so `ptln daemon restart` (or a reboot, since the unit is still installed) brings it back.
// Distinct from uninstallService, which removes the unit entirely.
//
// A stop is deliberately NOT a kill of in-flight work: crank children are detached into their own
// process groups and report to the control plane directly, so they run to completion and their
// results still land. Stopping means "accept no NEW work here", not "abandon what's running".
func stopService() error {
	if !serviceInstalled() {
		return fmt.Errorf("no always-on service installed — nothing to stop")
	}
	switch runtime.GOOS {
	case "darwin":
		// bootout unloads the job but leaves the plist on disk, so it comes back on restart/login.
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceLabel())
		if err := exec.Command("launchctl", "bootout", target).Run(); err != nil {
			return fmt.Errorf("launchctl bootout %s failed: %w", target, err)
		}
		return nil
	case "linux":
		if err := exec.Command("systemctl", "--user", "stop", systemdUnitName()).Run(); err != nil {
			return fmt.Errorf("systemctl --user stop %s failed: %w", systemdUnitName(), err)
		}
		return nil
	}
	return fmt.Errorf("stop not supported on %s", runtime.GOOS)
}

// installService writes + loads the per-user service. Requires an enrolled device (so the
// background process has a token). Returns a human-readable note on success. Idempotent-ish:
// reinstalling rewrites the unit (e.g. to pick up a new binary path after an upgrade).
func installService() (string, error) {
	if d := loadDaemonDevice(); d.Token == "" {
		return "", fmt.Errorf("device not enrolled — run `ptln daemon enable` first")
	}
	if err := os.MkdirAll(filepath.Dir(serviceLogPath()), 0o700); err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd()
	case "linux":
		return installSystemd()
	}
	return "", fmt.Errorf("always-on isn't supported on %s yet — run `ptln daemon run` yourself", runtime.GOOS)
}

func uninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	}
	return fmt.Errorf("nothing to uninstall on %s", runtime.GOOS)
}

// ---- macOS / launchd ----

// serviceEnvNames are the environment variables carried from the installing shell into the unit, IF
// they are set. PATH is unconditional (a service's inherited PATH is otherwise minimal, and the agents
// this spawns need the shell's one). The rest are opt-in: an unset variable is left out entirely so the
// unit says nothing about it.
//
// The consult caps are here because they were documented as the machine-wide way to stop auto-answer
// while being invisible to the very install that most people run. Baking them closes that gap for
// install-time intent — but this is a CONVENIENCE, not the safety mechanism: an env var frozen at
// install time can't be flipped later without a reinstall, so the real off switch is the persisted
// setting (`ptln daemon consults --all ask`, consult_policy.go), which is re-read per question.
var serviceEnvNames = []string{
	"PARTYLINE_API",
	envConsultAutoDaily,      // PARTYLINE_CONSULT_AUTO_DAILY
	envConsultAutoDailyTotal, // PARTYLINE_CONSULT_AUTO_DAILY_TOTAL
}

// launchdPlist renders the LaunchAgent. KeepAlive+RunAtLoad = "always on". PATH and the
// serviceEnvNames that are set are baked from the current environment so the service (and the agents
// it spawns) match the shell you installed from.
func launchdPlist() string {
	exe := selfExe()
	log := serviceLogPath()
	env := fmt.Sprintf("    <key>PATH</key><string>%s</string>\n", xmlEsc(os.Getenv("PATH")))
	for _, name := range serviceEnvNames {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			env += fmt.Sprintf("    <key>%s</key><string>%s</string>\n", name, xmlEsc(v))
		}
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>
%s  </dict>
</dict>
</plist>
`, serviceLabel(), xmlEsc(exe), xmlEsc(log), xmlEsc(log), env)
}

func installLaunchd() (string, error) {
	p := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(launchdPlist()), 0o644); err != nil {
		return "", err
	}
	dom := fmt.Sprintf("gui/%d", os.Getuid())
	// bootout a stale instance first (ignore errors), then bootstrap the fresh unit. Fall back
	// to the legacy load -w on older macOS where bootstrap isn't available.
	_ = exec.Command("launchctl", "bootout", dom+"/"+serviceLabel()).Run()
	if err := exec.Command("launchctl", "bootstrap", dom, p).Run(); err != nil {
		if e2 := exec.Command("launchctl", "load", "-w", p).Run(); e2 != nil {
			return "", fmt.Errorf("launchctl bootstrap/load failed (%v); plist written to %s", err, p)
		}
	}
	return "installed launchd agent " + serviceLabel() + " (logs: " + serviceLogPath() + ")", nil
}

func uninstallLaunchd() error {
	p := launchdPlistPath()
	dom := fmt.Sprintf("gui/%d", os.Getuid())
	if err := exec.Command("launchctl", "bootout", dom+"/"+serviceLabel()).Run(); err != nil {
		_ = exec.Command("launchctl", "unload", "-w", p).Run() // legacy fallback
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---- Linux / systemd --user ----

func systemdUnit() string {
	exe := selfExe()
	env := "Environment=PATH=" + os.Getenv("PATH")
	for _, name := range serviceEnvNames {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			env += "\nEnvironment=" + name + "=" + v
		}
	}
	return fmt.Sprintf(`[Unit]
Description=Partyline launch daemon (always-on)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s daemon run
Restart=always
RestartSec=5
%s

[Install]
WantedBy=default.target
`, exe, env)
}

func installSystemd() (string, error) {
	p := systemdUnitPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(systemdUnit()), 0o644); err != nil {
		return "", err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnitName()).Run(); err != nil {
		return "", fmt.Errorf("systemctl enable failed (%v); unit written to %s", err, p)
	}
	// Survive logout/reboot without an active session (best-effort; needs no sudo on most setups).
	_ = exec.Command("loginctl", "enable-linger").Run()
	return fmt.Sprintf("installed systemd --user unit %s (logs: journalctl --user -u %s)", systemdUnitName(), systemdUnitName()), nil
}

func uninstallSystemd() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName()).Run()
	if err := os.Remove(systemdUnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// xmlEsc escapes the few characters that matter inside a plist <string> value.
func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
