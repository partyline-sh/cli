//go:build darwin && tray

// BUILD TAG, not just `darwin`: this package needs cgo (systray binds Cocoa), and the release gate
// runs `go vet ./...` + `go test ./...` before goreleaser. With CGO_ENABLED=0 — which is how the CLI
// is built — those commands FAIL to compile systray and would break the release for a binary the
// pipeline doesn't even ship. Requiring an explicit tag keeps the tray out of every `./...` walk.
//
//	build it with:  CGO_ENABLED=1 go build -tags tray ./cmd/ptln-tray

// Command ptln-tray is the O.13 companion: ONE macOS menu bar icon showing what this machine is
// doing, with the controls to change it.
//
// THE GUARDRAIL (docs/ORCHESTRATOR-PLAN.md, O.13): the tray shows STATE and offers CONTROL. It never
// renders CONTENT. "2 agents waiting on you" and "claude · partyline · waiting" are state; the
// transcript, the diff, the todos, the review are content and belong in the web app. If you're about
// to add a scrollback pane, stop and add a deep link instead — that line is what keeps this a
// launcher rather than a second, worse client.
//
// ONE TRAY. `ptln llms` wakes this process rather than owning its own icon, and a file lock makes a
// second launch exit silently. What the menu contains varies with what's running; the icon does not
// multiply.
//
// It reimplements NOTHING: one `ptln state` call gives account + daemon + live sessions, and every
// action shells the CLI.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fyne.io/systray"
)

//go:embed icon.png
var iconPNG []byte

const (
	webBase      = "https://partyline.sh"
	pollInterval = 4 * time.Second
	maxRows      = 6 // session rows pre-allocated; systray can't grow a menu after start
)

// ---- state (mirrors `ptln state`; unknown fields ignored so a newer CLI never breaks an older tray)

type account struct {
	LoggedIn bool   `json:"logged_in"`
	Email    string `json:"email"`
}

type daemon struct {
	Enabled    bool `json:"enabled"`
	Installed  bool `json:"installed"`
	Active     bool `json:"active"`
	AutoUpdate bool `json:"auto_update"`
}

type session struct {
	ID     string `json:"id"`
	Tool   string `json:"tool"`
	Dir    string `json:"dir"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type machine struct {
	Version  string    `json:"version"`
	Account  account   `json:"account"`
	Daemon   daemon    `json:"daemon"`
	Sessions []session `json:"sessions"`
	Waiting  int       `json:"waiting"`
}

type readFail int

const (
	readOK     readFail = iota
	readNoCLI           // `ptln` isn't on PATH at all
	readTooOld          // present, but predates `ptln state`
)

// readState shells the CLI for the machine snapshot. The two failure modes stay DISTINCT because
// they need different advice: reporting an old CLI as "not found" sends you hunting for a binary
// that's sitting right there.
func readState() (machine, readFail) {
	out, err := exec.Command("ptln", "state").Output()
	if err != nil {
		if _, lookErr := exec.LookPath("ptln"); lookErr != nil {
			return machine{}, readNoCLI
		}
		return machine{}, readTooOld
	}
	var m machine
	if err := json.Unmarshal(out, &m); err != nil {
		return machine{}, readTooOld
	}
	return m, readOK
}

func ptln(args ...string) { _ = exec.Command("ptln", args...).Run() }
func openWeb(path string) { _ = exec.Command("open", webBase+path).Run() }

// termRun opens a Terminal window running a command. Used ONLY for sign-in, which genuinely has
// nowhere else to happen and has no existing window to steal focus from. Session rows deliberately
// do NOT use this — see the note where the rows are built.
func termRun(cmd string) {
	_ = exec.Command("osascript", "-e",
		fmt.Sprintf("tell application \"Terminal\" to do script %q\ntell application \"Terminal\" to activate", cmd)).Run()
}

// quietPath marks "no desktop notifications". The tray BADGE is the always-on signal; the banner is
// the interrupting one, so it's the one that gets a switch. Toggled from the menu, persisted here so
// it survives a restart.
func quietPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "tray-quiet")
}

func quiet() bool { _, err := os.Stat(quietPath()); return err == nil }

func setQuiet(on bool) {
	p := quietPath()
	if on {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte("quiet\n"), 0o600)
		return
	}
	_ = os.Remove(p)
}

// notify posts a macOS notification for exactly ONE thing — the waiting count RISING — because
// that's the state that costs real wall-clock while you're looking elsewhere.
//
// It is deliberately INERT: no link, no action, nothing that opens or launches. A notification that
// starts something is a trap here, because the session you'd be "opening" is already running in a
// terminal somewhere — acting on it spawns a SECOND client against the same session rather than
// taking you to the first. So the banner says where to look and gets out of the way.
func notify(body string) {
	if quiet() {
		return
	}
	_ = exec.Command("osascript", "-e",
		fmt.Sprintf("display notification %q with title \"Partyline\" sound name \"Submarine\"", body)).Run()
}

// lockSingleInstance holds an exclusive flock for this process's lifetime. `ptln llms` wakes the
// tray and a LaunchAgent may already run one — without this you'd collect a menu bar full of
// identical icons. flock rather than a pidfile because the kernel releases it when the process dies,
// so a crash can never leave a stale lock that blocks the tray forever.
func lockSingleInstance() bool {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".partyline")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true // can't lock → assume we're alone rather than refusing to start
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false // another tray holds it
	}
	return true // f is deliberately leaked: the lock must live as long as the process
}

func main() {
	if !lockSingleInstance() {
		return // a tray is already showing — exit silently, never a second icon
	}
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetTemplateIcon(iconPNG, iconPNG)

	mTitle := systray.AddMenuItem("Partyline", "partyline")
	mTitle.Disable()
	mAcct := systray.AddMenuItem("", "signed-in account")
	mAcct.Disable()
	systray.AddSeparator()

	// Live sessions. systray can't grow a menu after start, so rows are pre-allocated and shown or
	// hidden per poll.
	mSessHdr := systray.AddMenuItem("", "live AI sessions")
	mSessHdr.Disable()
	// Session rows are STATUS, not buttons. They used to open a Terminal running `ptln llms resume`,
	// which doesn't take you to the session — it starts a SECOND client against it, re-triggering the
	// engine's trust prompt while the real session sits in whatever terminal you left it in. There's
	// no reliable way to focus an arbitrary existing terminal (iTerm, Terminal, tmux, ssh), so the
	// honest move is to report and let you go there yourself.
	rows := make([]*systray.MenuItem, maxRows)
	for i := range rows {
		rows[i] = systray.AddMenuItem("", "")
		rows[i].Disable()
		rows[i].Hide()
	}
	mMore := systray.AddMenuItem("", "")
	mMore.Disable()
	mMore.Hide()

	systray.AddSeparator()
	mDaemon := systray.AddMenuItem("", "daemon status")
	mDaemon.Disable()
	mStart := systray.AddMenuItem("Start daemon", "start the always-on service")
	mStop := systray.AddMenuItem("Stop daemon", "stop it, keep it installed")
	mAuto := systray.AddMenuItem("Auto-update while idle", "keep this machine current")
	mNotify := systray.AddMenuItem("Desktop notifications", "banner when an agent starts waiting on you")

	systray.AddSeparator()
	mBoard := systray.AddMenuItem("Open board", "the work, in the web app")
	mFleet := systray.AddMenuItem("Open fleet", "every machine on this account")
	mLogin := systray.AddMenuItem("Sign in…", "sign in to partyline")

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit tray", "closes the icon; the daemon keeps running")

	lastWaiting := -1 // -1 = first pass, so we never announce pre-existing waits
	tip := ""

	refresh := func() {
		m, fail := readState()

		switch fail {
		case readNoCLI:
			mTitle.SetTitle("Partyline — ptln not installed")
			mAcct.SetTitle("")
		case readTooOld:
			mTitle.SetTitle("Partyline")
			mAcct.SetTitle("ptln too old — run: ptln update")
		default:
			mTitle.SetTitle("Partyline")
			if m.Account.LoggedIn {
				mAcct.SetTitle(m.Account.Email)
				mLogin.Hide()
			} else {
				mAcct.SetTitle("not signed in")
				mLogin.Show()
			}
		}

		// The badge beside the icon is reserved for the ONE number worth interrupting for. Putting
		// anything else there would turn it into wallpaper.
		if m.Waiting > 0 {
			systray.SetTitle(fmt.Sprintf("%d", m.Waiting))
		} else {
			systray.SetTitle("")
		}

		// Notify only on a RISE, and never on the first pass — otherwise launching the tray would
		// announce every session that was already blocked before you started it.
		if lastWaiting >= 0 && m.Waiting > lastWaiting {
			verb := "agent is"
			if m.Waiting > 1 {
				verb = "agents are"
			}
			notify(fmt.Sprintf("%d %s waiting on you — check your terminal or the session manager", m.Waiting, verb))
		}
		lastWaiting = m.Waiting

		switch {
		case len(m.Sessions) == 0:
			tip = "No sessions running"
		case m.Waiting > 0:
			tip = fmt.Sprintf("%d sessions · %d waiting on you", len(m.Sessions), m.Waiting)
		default:
			tip = fmt.Sprintf("%d sessions · all working", len(m.Sessions))
		}
		mSessHdr.SetTitle(tip)

		for i := range rows {
			if i < len(m.Sessions) {
				s := m.Sessions[i]
				mark := "▸" // working
				if s.Status == "waiting" {
					mark = "●" // your move
				}
				label := fmt.Sprintf("%s %s · %s", mark, s.Tool, s.Dir)
				if s.Title != "" {
					label += " — " + s.Title
				}
				rows[i].SetTitle(strings.TrimSpace(label))
				rows[i].Show()
			} else {
				rows[i].Hide()
			}
		}
		if extra := len(m.Sessions) - maxRows; extra > 0 {
			mMore.SetTitle(fmt.Sprintf("…and %d more", extra))
			mMore.Show()
		} else {
			mMore.Hide()
		}

		// Daemon line + controls. Only the action that can actually do something is shown, so a
		// click is never a silent no-op.
		switch {
		case !m.Daemon.Enabled:
			mDaemon.SetTitle("Daemon: not enrolled")
			mStart.Hide()
			mStop.Hide()
		case !m.Daemon.Installed:
			mDaemon.SetTitle("Daemon: not installed as a service")
			mStart.Hide()
			mStop.Hide()
		case m.Daemon.Active:
			mDaemon.SetTitle("Daemon: ● connected")
			mStart.Hide()
			mStop.Show()
		default:
			mDaemon.SetTitle("Daemon: ○ stopped")
			mStart.Show()
			mStop.Hide()
		}
		if m.Daemon.AutoUpdate {
			mAuto.Check()
		} else {
			mAuto.Uncheck()
		}
		if quiet() {
			mNotify.Uncheck()
		} else {
			mNotify.Check()
		}
		systray.SetTooltip("partyline — " + tip)
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
			case <-mLogin.ClickedCh:
				termRun("ptln login")
			case <-mStart.ClickedCh:
				ptln("daemon", "restart") // restart starts a stopped-but-installed service
				refresh()
			case <-mStop.ClickedCh:
				ptln("daemon", "stop")
				refresh()
			case <-mAuto.ClickedCh:
				// Toggle against OBSERVED state, not the checkbox: the setting is also changeable
				// from a terminal, and trusting the mark would flip it the wrong way.
				if m, fail := readState(); fail == readOK && m.Daemon.AutoUpdate {
					ptln("daemon", "autoupdate", "off")
				} else {
					ptln("daemon", "autoupdate", "on")
				}
				refresh()
			case <-mNotify.ClickedCh:
				setQuiet(!quiet()) // badge stays either way; this only silences the banner
				refresh()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
