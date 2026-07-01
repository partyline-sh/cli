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
	"runtime"
	"strings"
)

const serviceLabel = "sh.partyline.daemon"

func serviceLogPath() string { return filepath.Join(stateDir(), "daemon", "service.log") }

func launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func systemdUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "partyline-daemon.service")
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
		return exec.Command("launchctl", "list", serviceLabel).Run() == nil
	case "linux":
		return exec.Command("systemctl", "--user", "is-active", "--quiet", "partyline-daemon").Run() == nil
	}
	return false
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

// launchdPlist renders the LaunchAgent. KeepAlive+RunAtLoad = "always on". PATH and
// PARTYLINE_API are baked from the current environment so the service (and the agents it
// spawns) match the shell you installed from — a service's inherited PATH is otherwise minimal.
func launchdPlist() string {
	exe := selfExe()
	log := serviceLogPath()
	env := fmt.Sprintf("    <key>PATH</key><string>%s</string>\n", xmlEsc(os.Getenv("PATH")))
	if api := strings.TrimSpace(os.Getenv("PARTYLINE_API")); api != "" {
		env += fmt.Sprintf("    <key>PARTYLINE_API</key><string>%s</string>\n", xmlEsc(api))
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
`, serviceLabel, xmlEsc(exe), xmlEsc(log), xmlEsc(log), env)
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
	_ = exec.Command("launchctl", "bootout", dom+"/"+serviceLabel).Run()
	if err := exec.Command("launchctl", "bootstrap", dom, p).Run(); err != nil {
		if e2 := exec.Command("launchctl", "load", "-w", p).Run(); e2 != nil {
			return "", fmt.Errorf("launchctl bootstrap/load failed (%v); plist written to %s", err, p)
		}
	}
	return "installed launchd agent " + serviceLabel + " (logs: " + serviceLogPath() + ")", nil
}

func uninstallLaunchd() error {
	p := launchdPlistPath()
	dom := fmt.Sprintf("gui/%d", os.Getuid())
	if err := exec.Command("launchctl", "bootout", dom+"/"+serviceLabel).Run(); err != nil {
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
	if api := strings.TrimSpace(os.Getenv("PARTYLINE_API")); api != "" {
		env += "\nEnvironment=PARTYLINE_API=" + api
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
	if err := exec.Command("systemctl", "--user", "enable", "--now", "partyline-daemon").Run(); err != nil {
		return "", fmt.Errorf("systemctl enable failed (%v); unit written to %s", err, p)
	}
	// Survive logout/reboot without an active session (best-effort; needs no sudo on most setups).
	_ = exec.Command("loginctl", "enable-linger").Run()
	return "installed systemd --user unit partyline-daemon (logs: journalctl --user -u partyline-daemon)", nil
}

func uninstallSystemd() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", "partyline-daemon").Run()
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
