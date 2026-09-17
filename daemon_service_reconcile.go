package main

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

// daemon_service_reconcile.go — removing the always-on unit an instance left behind when it moved.
//
// THE OBSERVED FAILURE. The service is named after the ADDRESS it was installed for
// (sh.partyline.daemon.<env>), so pointing a machine at the same instance's new hostname installs a
// second unit and abandons the first. The abandoned one keeps running, keeps failing to reach an
// endpoint that stopped listening, and keeps writing to a log nobody reads. On the machine this was
// written from, `launchctl list` showed exactly that: two partyline agents, one of them pointed at
// a host that had been switched off hours earlier.
//
// WHY NOT JUST RENAME THE UNIT AFTER THE INSTANCE ID. Because that renames the unit under every
// EXISTING install at once, orphaning the very thing this is trying to clean up — and it would put
// an opaque UUID in the name an operator reads in `launchctl list`. The address is the right name;
// what was missing is removing the old one.
//
// SCOPE IS PROVEN, NEVER GUESSED. Only a unit belonging to a PREVIOUS ADDRESS OF THE SAME INSTANCE
// is touched, established through the instance registry. Two genuinely different partylines on one
// machine (production and a local build, say) are a supported setup, and a sweep that removed "any
// other partyline unit" would break it.

// reconcileStaleServices removes always-on units left over from earlier addresses of the instance
// this machine is now pointed at, and reports what it removed.
//
// Best effort throughout: this is tidying, and a machine that cannot unload a unit must still
// finish enrolling.
func reconcileStaleServices() []string {
	id := api.InstanceIDFor(api.Base())
	if id == "" {
		// No confirmed identity means no proof of ownership, so nothing may be removed. An
		// instance too old to answer /.well-known/partyline lands here and keeps both units —
		// untidy, but never destructive.
		return nil
	}
	current := envSuffix()
	var removed []string
	for _, host := range api.HostsForInstance(id) {
		suffix := unitSuffixFor(host)
		if suffix == current {
			continue
		}
		if removeServiceFor(suffix) {
			removed = append(removed, serviceLabelFor(suffix))
		}
		// Drop the mapping either way: the address is not where this instance lives any more,
		// and leaving it would re-sweep the same unit on every login.
		api.ForgetHost(host)
	}
	return removed
}

// unitSuffixFor is envSuffix() for an arbitrary address, kept in step with it by construction.
func unitSuffixFor(host string) string {
	l := api.EnvLabelFor(host)
	if l == "" {
		return ""
	}
	s := strings.Trim(unitSafe.ReplaceAllString(l, "-"), "-")
	if s == "" {
		s = "custom"
	}
	return s
}

func serviceLabelFor(suffix string) string {
	if suffix == "" {
		return "sh.partyline.daemon"
	}
	return "sh.partyline.daemon." + suffix
}

func systemdUnitNameFor(suffix string) string {
	if suffix == "" {
		return "partyline-daemon"
	}
	return "partyline-daemon-" + suffix
}

// safeUnitSuffix is belt and braces on a value that reaches a file path and a launchctl argument.
// The suffix is derived from a registry file an operator can edit, so it is re-checked here rather
// than trusted because unitSuffixFor happens to sanitise today.
var safeUnitSuffix = regexp.MustCompile(`^[a-zA-Z0-9-]*$`)

// removeServiceFor unloads and deletes one environment's unit. Reports whether a unit file was
// actually removed, so the caller only announces real work.
func removeServiceFor(suffix string) bool {
	if !safeUnitSuffix.MatchString(suffix) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		label := serviceLabelFor(suffix)
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
		p := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		if _, err := os.Stat(p); err != nil {
			return false
		}
		return os.Remove(p) == nil
	case "linux":
		unit := systemdUnitNameFor(suffix)
		_ = exec.Command("systemctl", "--user", "disable", "--now", unit+".service").Run()
		p := filepath.Join(home, ".config", "systemd", "user", unit+".service")
		if _, err := os.Stat(p); err != nil {
			return false
		}
		if os.Remove(p) != nil {
			return false
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return true
	}
	return false
}
