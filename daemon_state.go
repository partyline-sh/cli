package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// daemon_state.go — `ptln daemon state` — the MACHINE-READABLE sibling of `daemon status`.
//
// Exists for the O.13 tray companion, which must never reimplement daemon logic (the plan's rule:
// "the existing Go binary — CLI under the hood, never reimplemented"). The tray is a separate,
// cgo-linked darwin binary and so cannot import this package; shelling out to one JSON command keeps
// exactly ONE implementation of "what is this daemon's state" and lets the tray stay dumb.
//
// Emits NO secrets: never the device token, never an absolute path — same invariant as the heartbeat
// snapshot. Enrolment is reported as a bool and the daemon id only, both already visible in `status`.

// daemonState is the JSON contract consumed by the tray. Additive-only: the tray tolerates unknown
// fields, and an older tray reading a newer CLI simply ignores what it doesn't know.
type daemonState struct {
	Enabled    bool   `json:"enabled"`     // enrolled (a device token exists locally)
	DaemonID   string `json:"daemon_id"`   // opaque id, safe to show
	Base       string `json:"base"`        // control-plane base URL
	Installed  bool   `json:"installed"`   // an always-on service unit exists
	Active     bool   `json:"active"`      // the OS reports the service loaded/running
	Version    string `json:"version"`     // this binary's version
	AutoUpdate bool   `json:"auto_update"` // opted into idle self-update

	// Link is whether this machine is TALKING to its instance, which `Active` never answered:
	// Active is `launchctl list` succeeding, i.e. a registered job. A daemon crash-looping, failing
	// auth, or beating at an endpoint that stopped listening is Active and not connected. Additive,
	// so an older tray simply ignores it and keeps its previous (wrong, but unchanged) behaviour.
	Link linkHealth `json:"link"`
}

func currentDaemonState() daemonState {
	d := loadDaemonDevice()
	return daemonState{
		Enabled:    d.Token != "",
		DaemonID:   d.DaemonID,
		Base:       d.Base,
		Installed:  serviceInstalled(),
		Active:     serviceActive(),
		Version:    version,
		AutoUpdate: autoUpdateEnabled(),
		Link:       describeLink(serviceActive(), time.Now()),
	}
}

// daemonStateMain prints the state as one JSON object. Always exits 0 with valid JSON when it can —
// the tray polls this and a non-zero exit would read as "CLI missing" rather than "daemon stopped".
func daemonStateMain() {
	b, err := json.Marshal(currentDaemonState())
	if err != nil {
		fatal(fmt.Errorf("could not encode daemon state: %w", err))
	}
	fmt.Fprintln(os.Stdout, string(b))
}
