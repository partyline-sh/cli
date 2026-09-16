//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// tray_service.go — the tray's lifecycle. AUTOMATIC BY DEFAULT.
//
// The tray starts itself whenever the daemon or the session manager starts. It is NOT something you
// have to remember to turn on: an icon you must opt into is an icon most people never see, which
// defeats the entire point of a visibility surface.
//
// So the marker file here is an OPT-OUT (tray.off), not an opt-in. Absent = the tray auto-starts.
// `ptln tray off` writes it and is respected everywhere; `ptln tray on` clears it and additionally
// installs a LaunchAgent so the icon is present even when nothing else is running.
//
// Deliberately a SEPARATE agent from the daemon's (sh.partyline.daemon): the tray is a UI convenience
// and the daemon does the work. Quitting the tray must never stop your runs, and stopping the daemon
// must not remove the icon that tells you it stopped.
//
// darwin-only by build tag — the tray itself is macOS-only (GNOME dropped legacy tray support), so
// there's nothing to install elsewhere and `ptln tray` isn't offered there at all.

// Per control plane, for the same reason the daemon's unit is (daemon_service.go): the agent bakes
// the environment, so one label means a staging install silently replaces the production one.
func trayLabel() string {
	if s := envSuffix(); s != "" {
		return "sh.partyline.tray." + s
	}
	return "sh.partyline.tray"
}

// trayOptOutPath marks "do not auto-start the tray". An OPT-OUT by design — see the file comment.
func trayOptOutPath() string { return filepath.Join(stateDir(), "tray.off") }

// trayAutoStartAllowed reports whether we may bring the icon up on our own. True unless the operator
// explicitly said no.
func trayAutoStartAllowed() bool {
	_, err := os.Stat(trayOptOutPath())
	return err != nil
}

func setTrayOptOut(off bool) error {
	p := trayOptOutPath()
	if off {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("off\n"), 0o600)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func trayPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", trayLabel()+".plist")
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
%s  </dict>
</dict>
</plist>
`, trayLabel(), xmlEsc(bin), xmlEsc(os.Getenv("PATH")), trayEnvExtra())
}

// trayEnvExtra carries PARTYLINE_API into the unit when it is set. Without it an installed staging
// tray would start with an empty environment at login and quietly report on PRODUCTION — an icon
// that lies about which fleet you are looking at is worse than no icon.
func trayEnvExtra() string {
	// PTLN_BIN is unconditional: the tray must call the ptln that INSTALLED it, not whatever PATH
	// happens to resolve to at login. Without it a staging tray reports the production CLI.
	out := fmt.Sprintf("    <key>PTLN_BIN</key><string>%s</string>\n", xmlEsc(selfExe()))
	if v := strings.TrimSpace(os.Getenv("PARTYLINE_API")); v != "" {
		out += fmt.Sprintf("    <key>PARTYLINE_API</key><string>%s</string>\n", xmlEsc(v))
	}
	return out
}

func trayOn() error {
	if err := setTrayOptOut(false); err != nil {
		return err
	}
	bin := ensureTrayApp() // the login item must launch the BUNDLE, or the app loses its identity
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
	_ = exec.Command("launchctl", "bootout", target+"/"+trayLabel()).Run()
	if err := exec.Command("launchctl", "bootstrap", target, p).Run(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w", err)
	}
	return nil
}

// trayOff is a REAL off switch: it stops the running icon, removes the login item, AND records the
// opt-out so the daemon/session manager stop bringing it back. Without that last part "off" would
// last only until the next `ptln llms`.
func trayOff() error {
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), trayLabel())).Run()
	if err := os.Remove(trayPlistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = exec.Command("pkill", "-f", "ptln-tray").Run() // take the current icon down now, not at reboot
	return setTrayOptOut(true)
}

// trayMain is `ptln tray [on|off]` — prints status with no argument.
func trayMain(args []string) {
	if len(args) == 0 {
		switch {
		case trayBinary() == "":
			fmt.Println("tray: unavailable — ptln-tray isn't on this machine")
			fmt.Println("  it ships with partyline; reinstall to get it")
		case !trayAutoStartAllowed():
			fmt.Println("tray: off — you turned it off; `ptln tray on` brings it back")
		case trayInstalled():
			fmt.Println("tray: on — always running (login item installed)")
		default:
			fmt.Println("tray: on — starts automatically with the daemon or session manager")
			fmt.Println("  `ptln tray on` also keeps it running when neither is up")
		}
		return
	}
	switch args[0] {
	case "on":
		if err := trayOn(); err != nil {
			fatal(err)
		}
		fmt.Println("✓ tray on — the icon is in your menu bar and returns at login")
	case "off":
		if err := trayOff(); err != nil {
			fatal(err)
		}
		fmt.Println("✓ tray off — icon removed and it won't come back on its own")
		fmt.Println("  your daemon and runs are unaffected")
	default:
		fatal(fmt.Errorf("usage: ptln tray [on|off]"))
	}
}
