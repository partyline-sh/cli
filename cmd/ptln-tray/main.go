//go:build darwin && tray

// BUILD TAG, not just `darwin`: this package needs cgo (systray binds Cocoa), and the release gate
// runs `go vet ./...` + `go test ./...` before goreleaser. With CGO_ENABLED=0 — which is how the CLI
// is built — those commands FAIL to compile systray and would break the release for a binary the
// pipeline doesn't even ship. Requiring an explicit tag keeps the tray out of every `./...` walk.
//
//	build it with:  CGO_ENABLED=1 go build -tags tray ./cmd/ptln-tray

// Command ptln-tray is the O.13 daemon-host companion: a macOS menu bar icon whose ONLY job is to
// make the `ptln daemon` visible and controllable without a terminal.
//
// THE GUARDRAIL (docs/ORCHESTRATOR-PLAN.md, O.13): this is never a session cockpit. It shows daemon
// LIFECYCLE and links out to the web app for anything about the work itself. Rendering runs, tasks,
// logs, or review here would duplicate the web app and put us on Agent Deck's turf — explicitly
// forbidden. If you're about to add a scrollback view, stop and add a deep link instead.
//
// It reimplements NOTHING. Every action shells out to the `ptln` binary already installed on this
// machine, and all state comes from one `ptln daemon state` JSON call. That keeps a single
// implementation of daemon behavior and lets this stay a dumb shell.
//
// Why a SEPARATE binary and not a build tag on the CLI: systray binds Cocoa, so it needs cgo, while
// the release pipeline builds `partyline` with CGO_ENABLED=0 — which is what lets one mac-mini
// cross-compile every darwin+linux target. Folding this in would force cgo on the CLI and break that.
// So this ships as an additive darwin-only artifact and the CLI stays pure Go.
package main

import (
	_ "embed"
	"encoding/json"
	"os/exec"
	"time"

	"fyne.io/systray"
)

// The menu bar glyph — the partyline dish, rendered monochrome with alpha. Registered as a TEMPLATE
// image so macOS inverts it correctly for light/dark menu bars and for the highlighted state; a
// full-color icon would look wrong in at least one of those and can't adapt.
//
//go:embed icon.png
var iconPNG []byte

// webBase is where every "show me the work" action goes. The tray never renders run state itself.
const webBase = "https://partyline.sh"

// pollInterval refreshes the menu from `ptln daemon state`. Cheap (one exec of a local binary that
// reads two files and asks launchctl) and slow enough to be invisible.
const pollInterval = 5 * time.Second

// state mirrors the JSON contract from `ptln daemon state`. Unknown fields are ignored, so a newer
// CLI never breaks an older tray.
type state struct {
	Enabled    bool   `json:"enabled"`
	DaemonID   string `json:"daemon_id"`
	Installed  bool   `json:"installed"`
	Active     bool   `json:"active"`
	Version    string `json:"version"`
	AutoUpdate bool   `json:"auto_update"`
}

// readState shells the CLI for current daemon state. A failure here means the `ptln` binary is
// missing or broken — reported as the zero state, which the menu renders as "CLI not found".
func readState() (state, bool) {
	out, err := exec.Command("ptln", "daemon", "state").Output()
	if err != nil {
		return state{}, false
	}
	var s state
	if err := json.Unmarshal(out, &s); err != nil {
		return state{}, false
	}
	return s, true
}

// ptln runs a CLI subcommand fire-and-forget. Errors are deliberately swallowed: this is a
// convenience surface, and the next poll shows whether the action actually took effect — which is
// more honest than a modal claiming success.
func ptln(args ...string) { _ = exec.Command("ptln", args...).Run() }

func openWeb(path string) { _ = exec.Command("open", webBase+path).Run() }

func main() { systray.Run(onReady, func() {}) }

func onReady() {
	systray.SetTemplateIcon(iconPNG, iconPNG)
	systray.SetTooltip("partyline daemon")

	// Status lines: disabled menu items used as read-only labels (systray has no label primitive).
	mStatus := systray.AddMenuItem("checking…", "current daemon state")
	mStatus.Disable()
	mVersion := systray.AddMenuItem("", "installed CLI version")
	mVersion.Disable()

	systray.AddSeparator()
	mBoard := systray.AddMenuItem("Open board", "see the work in the web app")
	mFleet := systray.AddMenuItem("Open fleet", "see every machine on this account")

	systray.AddSeparator()
	mRestart := systray.AddMenuItem("Restart daemon", "restart the always-on service")
	mStop := systray.AddMenuItem("Stop daemon", "stop the always-on service")

	systray.AddSeparator()
	mUpdate := systray.AddMenuItem("Check for updates", "upgrade the CLI now")
	mAuto := systray.AddMenuItem("Auto-update while idle", "keep this machine current automatically")

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit tray", "close this menu bar icon (the daemon keeps running)")

	// refresh repaints every label from one state read. The daemon's own lifecycle is the source of
	// truth — the tray never caches a decision it made itself, so an action taken elsewhere (a
	// terminal, another machine, the service crashing) shows up here within one poll.
	refresh := func() {
		s, ok := readState()
		label := ""
		switch {
		case !ok:
			label = "ptln not found on PATH"
		case !s.Enabled:
			label = "Not enrolled — run: ptln daemon enable"
		case !s.Installed:
			label = "Not installed as a service"
		case s.Active:
			label = "● Connected"
		default:
			label = "○ Stopped"
		}
		mStatus.SetTitle(label)
		if ok {
			mVersion.SetTitle("ptln " + s.Version)
		} else {
			mVersion.SetTitle("")
		}
		// Controls that can't do anything are disabled rather than silently no-oping.
		if s.Installed {
			mRestart.Enable()
			mStop.Enable()
		} else {
			mRestart.Disable()
			mStop.Disable()
		}
		if s.AutoUpdate {
			mAuto.Check()
		} else {
			mAuto.Uncheck()
		}
		systray.SetTooltip("partyline daemon — " + label)
	}
	refresh()

	go func() {
		t := time.NewTicker(pollInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh()
			case <-mBoard.ClickedCh:
				openWeb("/work")
			case <-mFleet.ClickedCh:
				openWeb("/fleet")
			case <-mRestart.ClickedCh:
				ptln("daemon", "restart")
				refresh()
			case <-mStop.ClickedCh:
				ptln("daemon", "stop")
				refresh()
			case <-mUpdate.ClickedCh:
				ptln("update")
				refresh()
			case <-mAuto.ClickedCh:
				// Toggle against OBSERVED state, not the checkbox: the setting can also be changed
				// from a terminal, and trusting the menu's own mark would flip it the wrong way.
				if s, ok := readState(); ok && s.AutoUpdate {
					ptln("daemon", "autoupdate", "off")
				} else {
					ptln("daemon", "autoupdate", "on")
				}
				refresh()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
