// Anonymous usage telemetry (Telemetry C) — how many installs are actually live in the wild.
// Deliberately minimal and honest:
//   - a random install-id (NOT an identity), stored locally in ~/.partyline/telemetry.json
//   - a once-a-day "active" ping carrying only {install_id, version, os} — no paths, no content,
//     no account, no project names
//   - opt-out via DO_NOT_TRACK or PARTYLINE_TELEMETRY=0 (also off for CI + dev builds)
//   - disclosed BEFORE we ever collect: the first ping waits for a one-time notice on an
//     interactive run, so a headless/mcp invocation never phones home undisclosed
//   - the CLI only ever talks to the control plane it is configured for, and only reports at all
//     when that IS partyline.sh — a self-hosted install (PARTYLINE_API elsewhere) sends nothing
package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/api"
)

type telemetryState struct {
	InstallID   string    `json:"install_id"`
	PingedAt    time.Time `json:"pinged_at"`
	NoticeShown bool      `json:"notice_shown"`
}

func telemetryStatePath() string { return filepath.Join(stateDir(), "telemetry.json") }

func loadTelemetryState() telemetryState {
	var s telemetryState
	if b, err := os.ReadFile(telemetryStatePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveTelemetryState(s telemetryState) {
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		_ = os.WriteFile(telemetryStatePath(), b, 0o600)
	}
}

// telemetryDisabled honors the standard opt-outs (DO_NOT_TRACK), a partyline-specific switch, CI,
// and dev builds — the same conservative posture as the update check.
//
// It is also off, unconditionally, when this CLI is pointed at a self-hosted control plane. That
// check comes FIRST and no env var re-enables it (not even PARTYLINE_TELEMETRY=1): self-hosting is
// not a preference, it is a different operator, and their usage is not ours to count.
func telemetryDisabled() bool {
	if api.IsSelfHosted() {
		return true
	}
	if version == "dev" || os.Getenv("CI") != "" || os.Getenv("DO_NOT_TRACK") != "" {
		return true
	}
	switch os.Getenv("PARTYLINE_TELEMETRY") {
	case "0", "false", "off", "no", "FALSE", "OFF", "NO":
		return true
	}
	return false
}

// newInstallID is a random v4-shaped UUID — an opaque install marker, never derived from anything
// identifying. Empty on the (astronomically unlikely) rand failure, which just skips this ping.
func newInstallID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func printTelemetryNotice() {
	dim := func(s string) string { return "\x1b[38;5;245m" + s + "\x1b[0m" }
	fmt.Fprintln(os.Stderr, dim("☎ partyline sends an anonymous once-a-day usage ping (a random id + version + OS —"))
	fmt.Fprintln(os.Stderr, dim("  no identity, no paths, no content) so we can see how many installs are active."))
	fmt.Fprintln(os.Stderr, dim("  opt out anytime:  export PARTYLINE_TELEMETRY=0   ·   docs: https://partyline.sh/docs/telemetry"))
}

// maybeTelemetryPing fires the throttled anonymous ping (≤1/day). Non-blocking: the actual POST runs
// in a goroutine, so a short command may exit before it lands — the throttle just retries next run.
// Collection never happens before the one-time notice has been shown on an interactive terminal.
func maybeTelemetryPing() {
	if telemetryDisabled() {
		return
	}
	st := loadTelemetryState()
	dirty := false
	if st.InstallID == "" {
		if id := newInstallID(); id != "" {
			st.InstallID, dirty = id, true
		} else {
			return
		}
	}
	// Disclose before we ever collect. If we haven't shown the notice and this run isn't
	// interactive (e.g. spawned headless / piped), defer — don't phone home undisclosed.
	if !st.NoticeShown {
		if !term.IsTerminal(int(os.Stderr.Fd())) {
			if dirty {
				saveTelemetryState(st)
			}
			return
		}
		printTelemetryNotice()
		st.NoticeShown, dirty = true, true
	}
	due := time.Since(st.PingedAt) >= 24*time.Hour
	if dirty {
		saveTelemetryState(st)
	}
	if !due {
		return
	}
	id := st.InstallID
	go func() {
		if err := api.New().SendTelemetry(id, version, runtime.GOOS); err == nil {
			s := loadTelemetryState()
			s.PingedAt = time.Now()
			saveTelemetryState(s)
		}
	}()
}
