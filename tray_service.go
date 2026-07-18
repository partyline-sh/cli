//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// tray_service.go — `ptln tray on|off|status`, the login-item side of the O.13 menu bar companion.
//
// Shipping the ptln-tray binary in the archive puts it on disk; it doesn't make it APPEAR. Without a
// login item you'd relaunch it by hand after every reboot, which is the opposite of "a visible local
// presence". This installs a LaunchAgent so it starts at login and comes back if it's killed.
//
// Deliberately a SEPARATE agent from the daemon's (sh.partyline.daemon): the tray is a UI convenience
// and the daemon does the work. Quitting the tray must never stop your runs, and stopping the daemon
// must not remove the icon that tells you it stopped.
//
// darwin-only by build tag — the tray itself is macOS-only (GNOME dropped legacy tray support), so
// there's nothing to install elsewhere and `ptln tray` isn't offered there at all.

const trayLabel = "sh.partyline.tray"

func trayPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", trayLabel+".plist")
}

// trayBinary resolves the ptln-tray executable — it ships in the same archive, so it sits next to the
// running CLI. Falls back to PATH for a manually-placed build. "" when it genuinely isn't installed.
func trayBinary() string {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		cand := filepath.Join(filepath.Dir(exe), "ptln-tray")
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath("ptln-tray"); err == nil {
		return p
	}
	return ""
}

func trayInstalled() bool {
	_, err := os.Stat(trayPlistPath())
	return err == nil
}

// trayPlist renders the login item. KeepAlive brings the icon back if the process dies; RunAtLoad
// starts it now and at every login. PATH is inherited from the installing shell because the tray
// shells out to `ptln` for everything it does — a LaunchAgent's default PATH would not find it.
func trayPlist(bin string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>%s</string>
  </dict>
</dict>
</plist>
`, trayLabel, xmlEsc(bin), xmlEsc(os.Getenv("PATH")))
}

func trayOn() error {
	bin := trayBinary()
	if bin == "" {
		return fmt.Errorf("ptln-tray isn't installed next to ptln — reinstall partyline (it ships in the same archive)")
	}
	p := trayPlistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(trayPlist(bin)), 0o644); err != nil {
		return err
	}
	// bootout first so a reinstall picks up a new binary path instead of leaving the old job loaded.
	target := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", target+"/"+trayLabel).Run()
	if err := exec.Command("launchctl", "bootstrap", target, p).Run(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w", err)
	}
	return nil
}

func trayOff() error {
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), trayLabel)).Run()
	if err := os.Remove(trayPlistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// trayMain is `ptln tray [on|off]` — prints status with no argument.
func trayMain(args []string) {
	if len(args) == 0 {
		switch {
		case trayInstalled():
			fmt.Println("tray: on — the menu bar icon starts at login")
		case trayBinary() == "":
			fmt.Println("tray: not installed — ptln-tray isn't on this machine")
			fmt.Println("  it ships with partyline; reinstall to get it")
		default:
			fmt.Println("tray: off — turn it on with `ptln tray on`")
		}
		return
	}
	switch args[0] {
	case "on":
		if err := trayOn(); err != nil {
			fatal(err)
		}
		fmt.Println("✓ tray on — the partyline icon is in your menu bar, and returns at login")
	case "off":
		if err := trayOff(); err != nil {
			fatal(err)
		}
		fmt.Println("✓ tray off — icon removed; your daemon and runs are unaffected")
	default:
		fatal(fmt.Errorf("usage: ptln tray [on|off]"))
	}
}
