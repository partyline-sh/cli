package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"partyline.sh/partyline/internal/api"
)

// autoupdate.go — OPT-IN self-update for an always-on daemon.
//
// Why this exists: every capability change ships in the binary, so a fleet that only updates by hand
// drifts out of sync fast and features silently no-op on stale nodes (a daemon that doesn't know
// `--base` quietly keeps forking from the repo default). Releasing often is fine; MANUAL adoption is
// what actually causes drift.
//
// What this deliberately is NOT: the control plane never sends behavior, code, or an artifact URL. The
// daemon checks the same public version endpoint every install already polls and runs the same
// installer the operator originally used. Everyone converges on the SAME public release — a
// fleet-uniform artifact, never a per-tenant instruction. That distinction is the whole security line:
// a compromised control plane still cannot make one customer's machine run something bespoke.
//
// Five guards, all of which must pass:
//  1. the operator opted in           (ptln daemon autoupdate on)
//  2. update checks aren't disabled   (respects the existing PTLN_NO_UPDATE_CHECK opt-out)
//  3. the daemon is service-managed   (something must restart us into the new binary)
//  4. no run is in flight             (don't swap the binary under active work)
//  5. the published version is NEWER  (never sidegrade or downgrade)

// autoUpdateStatePath is the per-node opt-in marker — same shape as provision.on: a plain marker file
// the operator flips, kept out of the registry (owner-authored project bindings) and device.json
// (identity). Its existence IS the setting.
func autoUpdateStatePath() string { return filepath.Join(daemonDir(), "autoupdate.on") }

// autoUpdateEnabled reports whether this node opted into self-update.
func autoUpdateEnabled() bool {
	_, err := os.Stat(autoUpdateStatePath())
	return err == nil
}

func setAutoUpdateEnabled(on bool) error {
	p := autoUpdateStatePath()
	if on {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("on\n"), 0o600)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// runsInFlight reports how many crank children this daemon is currently tracking. Zero = idle.
func runsInFlight() int {
	runProcsMu.Lock()
	defer runProcsMu.Unlock()
	return len(runProcs)
}

// autoUpdateInterval is how often an opted-in daemon checks for a newer release. Deliberately far
// slower than the 60s heartbeat: the version endpoint is cached server-side and a node being a few
// hours behind is not the problem this solves — a node being WEEKS behind is.
const autoUpdateInterval = 6 * time.Hour

// startAutoUpdate runs the opt-in self-update loop until ctx ends. Always safe to call: every guard
// is re-evaluated per tick, so flipping the marker file takes effect without a restart.
func startAutoUpdate(ctx context.Context) {
	go func() {
		// Wait one interval before the first check — a daemon that just started (very likely because
		// it was JUST upgraded) shouldn't immediately try to upgrade again.
		t := time.NewTicker(autoUpdateInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				autoUpdateTick()
			}
		}
	}()
}

// autoUpdateTick performs one guarded check-and-upgrade. Best-effort throughout: any failure just
// leaves the daemon on its current version to try again next tick. On success this process is
// REPLACED (the service restarts), so nothing after applyUpgrade runs.
func autoUpdateTick() {
	if !autoUpdateEnabled() || updateChecksDisabled() {
		return
	}
	// A daemon not under a service manager has nothing to restart it into the new binary — it would
	// swap the file on disk and keep running the old code forever, reporting a version it isn't. Skip
	// rather than lie about what's running.
	if !serviceInstalled() {
		return
	}
	if runsInFlight() > 0 {
		return // busy: never swap the binary under active work
	}
	latest, _, _, err := api.New().LatestVersion(version, runtime.GOOS)
	if err != nil || latest == "" || !versionLess(version, latest) {
		return
	}
	// Re-check idleness as late as possible: a run may have been dispatched while the version lookup
	// was in flight. (A detached crank child would actually SURVIVE the restart — it has its own
	// process group and reports to the API directly — but deferring is still the honest choice.)
	if runsInFlight() > 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "☎ auto-update: %s → %s\n", version, latest)
	if err := applyUpgrade(); err != nil {
		fmt.Fprintf(os.Stderr, "☎ auto-update failed (staying on %s): %v\n", version, err)
		return
	}
	// The binary on disk is new; this process is still the old one. Bounce the service so it re-execs.
	if err := restartService(); err != nil {
		fmt.Fprintf(os.Stderr, "☎ auto-update: upgraded on disk but couldn't restart (%v) — run `ptln daemon restart`\n", err)
	}
}

// webUpdateTick is the "update now" nudge from the web (fleet page): identical to autoUpdateTick
// EXCEPT it does not require the autoupdate opt-in — the click IS explicit operator intent, so the
// standing-consent marker is beside the point. Every machine-protective guard stays: the local
// PTLN_NO_UPDATE_CHECK opt-out (a teammate's click must not override this machine's owner),
// service-managed only (something must restart us into the new binary), idle only (never swap the
// binary under active work), and newer-only (never sidegrade/downgrade). Still the same public
// release + installer every install uses — the web sent a nudge, not a payload.
func webUpdateTick() {
	if updateChecksDisabled() {
		fmt.Fprintf(os.Stderr, "☎ web update request ignored: PTLN_NO_UPDATE_CHECK is set on this machine\n")
		return
	}
	if !serviceInstalled() {
		fmt.Fprintf(os.Stderr, "☎ web update request ignored: not the always-on service (nothing would restart into the new binary)\n")
		return
	}
	if runsInFlight() > 0 {
		fmt.Fprintf(os.Stderr, "☎ web update request deferred: runs in flight — click again when idle\n")
		return
	}
	latest, _, _, err := api.New().LatestVersion(version, runtime.GOOS)
	if err != nil || latest == "" || !versionLess(version, latest) {
		fmt.Fprintf(os.Stderr, "☎ web update request: already current (%s)\n", version)
		return
	}
	if runsInFlight() > 0 { // re-check as late as possible (a run may have arrived meanwhile)
		return
	}
	fmt.Fprintf(os.Stderr, "☎ web-requested update: %s → %s\n", version, latest)
	if err := applyUpgrade(); err != nil {
		fmt.Fprintf(os.Stderr, "☎ update failed (staying on %s): %v\n", version, err)
		return
	}
	if err := restartService(); err != nil {
		fmt.Fprintf(os.Stderr, "☎ upgraded on disk but couldn't restart (%v) — run `ptln daemon restart`\n", err)
	}
}

// upgradeCommand builds the install-appropriate upgrade command. Shared by the interactive
// `ptln upgrade` and the daemon's auto-update so both paths can never diverge.
func upgradeCommand() *exec.Cmd {
	if installedViaBrew() {
		return exec.Command("brew", "upgrade", "partyline")
	}
	return exec.Command("sh", "-c", "curl -fsSL https://partyline.sh/install.sh | sh")
}

// applyUpgrade runs the upgrade NON-interactively (no stdin, output to stderr for the service log) —
// the auto-update path. `ptln upgrade` keeps its own terminal-inheriting version.
func applyUpgrade() error {
	cmd := upgradeCommand()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// daemonAutoUpdate is `ptln daemon autoupdate [on|off]` — prints status with no argument.
func daemonAutoUpdate(args []string) {
	if len(args) == 0 {
		if autoUpdateEnabled() {
			fmt.Printf("auto-update: on (checks every %s while idle)\n", autoUpdateInterval)
			if !serviceInstalled() {
				fmt.Println("  ⚠ this machine isn't running the daemon as a service, so auto-update stays inactive")
				fmt.Println("    install it with: ptln daemon install")
			}
		} else {
			fmt.Println("auto-update: off — this daemon stays on", version, "until you run `ptln update`")
		}
		return
	}
	switch args[0] {
	case "on":
		if err := setAutoUpdateEnabled(true); err != nil {
			fatal(fmt.Errorf("could not enable auto-update: %w", err))
		}
		fmt.Println("✓ auto-update on — this daemon upgrades itself to new releases while idle")
		if !serviceInstalled() {
			fmt.Println("  ⚠ not running as a service yet, so nothing can restart into the new binary")
			fmt.Println("    install it with: ptln daemon install")
		}
	case "off":
		if err := setAutoUpdateEnabled(false); err != nil {
			fatal(fmt.Errorf("could not disable auto-update: %w", err))
		}
		fmt.Println("✓ auto-update off — upgrade this daemon with `ptln update`")
	default:
		fatal(fmt.Errorf("usage: ptln daemon autoupdate [on|off]"))
	}
}
