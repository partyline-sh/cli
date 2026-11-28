package api

// machineid.go — a stable identity for THIS MACHINE, so re-registering a daemon updates its row
// instead of minting a new one.
//
// THE BUG THIS FIXES. `POST /api/v1/daemon/register` did a blind insert keyed on nothing: user_id,
// a cosmetic device_label, and a token hash. Every `ptln login` / re-enrol therefore created ANOTHER
// daemons row for the same physical machine. Observed in prod 2026-08-14: 11 rows for 3 machines
// (MacBook-Air.local x5, monolith x3, mini-6.local x2).
//
// It is not cosmetic clutter. A run pins `daemon_id` at enqueue, so every re-registration STRANDS
// the runs pointed at the old row — nothing is listening on that id ever again. Four runs sat at
// "Starting…" against a registration whose last heartbeat was 18.6 days old, and the board happily
// reported them as Building. The fleet page shows ghosts for the same reason.
//
// WHY NOT device_label. It is the hostname, it is user-editable, it is not unique across a team
// (two people can both have "MacBook-Air.local"), and it changes when someone renames their laptop —
// which would strand runs exactly like re-registering does.
//
// WHY NOT the telemetry install_id. That id is deliberately anonymous and explicitly NOT an identity
// (it exists so a daily ping can be counted without identifying anyone). Reusing it as a machine
// identity would quietly convert an anonymous counter into a stable per-account device key, which is
// the one thing its design promises it is not.
//
// WHAT WE SEND. A salted SHA-256 of the OS's own machine UUID — never the raw UUID. The server needs
// only "same machine or not"; a hash answers that completely, and a raw hardware identifier sitting
// in a database is a liability with no matching benefit. Stable across reinstalls and upgrades,
// changes when the OS is reimaged (a genuinely different machine, so a new row is correct).

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// machineIDSalt keeps the digest specific to partyline: the same machine hashed for some other
// purpose must not produce a value that can be cross-referenced with this one.
const machineIDSalt = "partyline-machine-v1:"

var uuidRe = regexp.MustCompile(`[0-9a-fA-F-]{16,}`)

// MachineID returns the salted hash, or "" when the platform gives us nothing trustworthy.
//
// Empty is a deliberate, meaningful answer: the server treats a missing machine_id as "cannot
// dedupe" and falls back to today's insert behaviour. Guessing an identity from something unstable
// (hostname, MAC, boot id) would be WORSE than not deduping — a value that changes on its own
// silently strands runs, which is the exact failure this file exists to prevent. Better to leave a
// duplicate a human can see than to invent an identity that drifts.
func MachineID() string {
	raw := rawMachineIdentifier()
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(machineIDSalt + raw))
	return hex.EncodeToString(sum[:])
}

// rawMachineIdentifier reads the OS's own persistent machine UUID. Never leaves this process.
func rawMachineIdentifier() string {
	switch runtime.GOOS {
	case "darwin":
		// IOPlatformUUID is assigned by the hardware and survives OS reinstall.
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "IOPlatformUUID") {
				continue
			}
			if m := uuidRe.FindString(strings.SplitN(line, "=", 2)[len(strings.SplitN(line, "=", 2))-1]); m != "" {
				return m
			}
		}
		return ""
	case "linux":
		// /etc/machine-id is the systemd standard; the dbus path is the fallback on older systems.
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if b, err := os.ReadFile(p); err == nil {
				if v := strings.TrimSpace(string(b)); v != "" {
					return v
				}
			}
		}
		return ""
	default:
		return ""
	}
}
