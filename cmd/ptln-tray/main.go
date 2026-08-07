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
// ONE TRAY PER CONTROL PLANE. `ptln llms` wakes this process rather than owning its own icon, and a
// file lock makes a second launch exit silently. What the menu contains varies with what's running;
// the icon does not multiply.
//
// The lock is scoped to api.ConfigDir, so production and a staging endpoint each get exactly one
// icon rather than fighting over a single machine-global one. That was the bug: a staging daemon
// started, its tray hit production's lock, and it exited — so the environment you were actually
// working in was the one you could never see.
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
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/systray"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/traypeer"
)

//go:embed icon.png
var iconPNG []byte

const (
	defaultWebBase = "https://partyline.sh"
	pollInterval   = 4 * time.Second
	maxRows        = 6 // session rows pre-allocated; systray can't grow a menu after start
)

// webBase is the web app the menu links open. It follows whatever control plane `ptln state`
// reports, because a tray that SAYS staging while "Open board" opens production is worse than no
// label at all. Written by the refresh loop and read by menu-click goroutines, so it is atomic
// rather than a plain string.
var webBase atomic.Value // string; seeded to production, so an older `ptln` behaves exactly as before

func init() { webBase.Store(defaultWebBase) }

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

type rateLimit struct {
	ResetAt string `json:"reset_at"`
	Note    string `json:"note"`
	Run     string `json:"run"`
}

// The peer snapshot (traypeer.Snapshot) mirrors `ptln state`'s peers object. Question text is the ONE
// piece of content the tray handles, and only for an inbound question: the daemon requires a digest of
// the text that was displayed before it will answer one, so showing it in the submenu is what makes a
// one-click approve honest rather than blind. See peer_rows.go and internal/traypeer.

type machine struct {
	Version string `json:"version"`
	// Env / API name the control plane. Absent against an older `ptln` — which reads as production,
	// the only thing an unlabelled tray has ever meant.
	Env       string     `json:"env"`
	API       string     `json:"api"`
	Account   account    `json:"account"`
	Daemon    daemon     `json:"daemon"`
	Sessions  []session  `json:"sessions"`
	Waiting   int        `json:"waiting"`
	RateLimit *rateLimit `json:"rate_limit"`
	// Peers is nil against a `ptln` that predates it — the key is omitted when there's nothing to
	// say, so "old CLI" and "nothing happening" look the same and both degrade to a hidden section.
	Peers *traypeer.Snapshot `json:"peers"`
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
	out, err := exec.Command(ptlnBin(), "state").Output()
	if err != nil {
		if _, lookErr := exec.LookPath(ptlnBin()); lookErr != nil {
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

// trayTitle renders the header row: which build this is, and which control plane it is talking to.
//
// Production stays "Partyline vX.Y.Z" — unlabelled means production, the way it always has. A
// non-production tray is labelled because the icon looks identical either way, and the actions
// behind it (start/stop the daemon, open the board) are not the ones you'd expect if you have
// mistaken staging for prod.
//
// A dev build is shown as-is: "Partyline dev". Prefixing a "v" onto a non-numeric version reads as
// a release that does not exist.
func trayTitle(version, env string) string {
	t := "Partyline"
	if version != "" {
		if version[0] >= '0' && version[0] <= '9' {
			t += " v" + version
		} else {
			t += " " + version
		}
	}
	if env != "" {
		t += " · " + env
	}
	return t
}

// ptlnBin resolves WHICH ptln to shell out to. The tray renders nothing of its own — version, env,
// sessions, the web base behind Open board / Open fleet all come from `ptln state` — so running the
// wrong binary means the whole menu describes the wrong control plane.
//
// It used to be a bare "ptln" from PATH. On a machine with production installed via Homebrew, a
// STAGING tray therefore reported production's version and production's URLs while claiming to be
// staging: PATH does not care which daemon started us.
//
// PTLN_BIN is set by whichever ptln spawned this tray (wakeTray) and baked into the LaunchAgent, so
// the tray follows its launcher. Falling back to PATH keeps a hand-started tray working.
func ptlnBin() string {
	if v := strings.TrimSpace(os.Getenv("PTLN_BIN")); v != "" {
		if fi, err := os.Stat(v); err == nil && !fi.IsDir() {
			return v
		}
	}
	return "ptln"
}

func ptln(args ...string) { _ = exec.Command(ptlnBin(), args...).Run() }
func openWeb(path string) { _ = exec.Command("open", webBase.Load().(string)+path).Run() }

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
	nativeNotify(body)
}

// lockSingleInstance holds an exclusive flock for this process's lifetime. `ptln llms` wakes the
// tray and a LaunchAgent may already run one — without this you'd collect a menu bar full of
// identical icons. flock rather than a pidfile because the kernel releases it when the process dies,
// so a crash can never leave a stale lock that blocks the tray forever.
// trayLockFile holds the flock'd file for the WHOLE process lifetime. It MUST be a package var, not a
// local: an *os.File that falls out of scope is garbage-collected, and os.File's finalizer calls
// Close() on the fd — which RELEASES the flock. That was the bug behind the menu-bar phone pileup: the
// first tray grabbed the lock, GC then quietly dropped it, and every later wakeTray spawn acquired it
// again → N icons. Keeping the reference alive here is what makes the singleton actually hold.
var trayLockFile *os.File

func lockSingleInstance() bool {
	// PER CONTROL PLANE. The lock used to live at ~/.partyline/tray.lock — machine-global — so a
	// staging tray exited silently the moment a production one was running, and you could never see
	// the environment you were actually working in. api.ConfigDir keeps production at ~/.partyline
	// exactly as before; every other endpoint gets its own directory, and its own icon.
	dir := api.ConfigDir()
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true // can't lock → assume we're alone rather than refusing to start
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return false // another tray holds it
	}
	trayLockFile = f // hold the reference (and thus the fd + flock) for the process lifetime
	return true
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
	// The provider refusing work is the single most important thing this machine can tell you, so it
	// sits directly under the title and hides entirely when there's nothing to say. A quiet fleet and
	// a STOPPED fleet look identical otherwise — which is how a 2.7M-token run went unnoticed.
	mLimit := systray.AddMenuItem("", "the model provider is refusing work")
	mLimit.Hide()

	systray.AddSeparator()

	// Peer messaging: teammates' questions that still need you, what this machine answered on its
	// own, and replies to your asks. Above the sessions because a person waiting on you outranks a
	// machine working. Hides itself entirely when there's nothing.
	peerSec := newPeerSection()

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

		// Point the menu links at whatever control plane the CLI is actually on, before any of them
		// can be clicked. Absent (older `ptln`) leaves the production default in place.
		if m.API != "" {
			webBase.Store(m.API)
		}

		switch fail {
		case readNoCLI:
			mTitle.SetTitle("Partyline — ptln not installed")
			mAcct.SetTitle("")
		case readTooOld:
			mTitle.SetTitle("Partyline")
			mAcct.SetTitle("ptln too old — run: ptln update")
		default:
			mTitle.SetTitle(trayTitle(m.Version, m.Env))
			if m.Account.LoggedIn {
				mAcct.SetTitle(m.Account.Email)
				mLogin.Hide()
			} else {
				mAcct.SetTitle("not signed in")
				mLogin.Show()
			}
		}

		// Rate limit line. Two genuinely different situations, so they get different words: a quota
		// window REOPENS on its own and you just wait, while an entitlement block needs a human to add
		// credits or enable the model. Telling you to wait for something that will never happen is
		// the worse failure, so the reset-less case says so plainly.
		if m.RateLimit != nil {
			switch {
			case m.RateLimit.ResetAt != "":
				if t, err := time.Parse(time.RFC3339, m.RateLimit.ResetAt); err == nil {
					mLimit.SetTitle("⏸ Rate limited — resets " + t.Local().Format("3:04 PM"))
				} else {
					mLimit.SetTitle("⏸ Rate limited")
				}
			case m.RateLimit.Note != "":
				mLimit.SetTitle("⏸ Blocked — " + m.RateLimit.Note)
			default:
				mLimit.SetTitle("⏸ Blocked — needs usage credits or model access")
			}
			mLimit.Show()
		} else {
			mLimit.Hide()
		}

		// Peer rows + their notifications. Before the badge, because a queued question counts toward it.
		// A failed read blanks the section but keeps the notification edges, so a CLI that went missing
		// for one tick doesn't re-announce every question when it comes back.
		peerSec.Poll(fail == readOK, m.Peers)

		// The badge beside the icon is reserved for the ONE number worth interrupting for: things
		// BLOCKED ON YOU. A teammate's question sitting in the approval queue is exactly that — a
		// person waiting — so it joins the count. Auto-answered consults and landed replies do NOT:
		// nothing is blocked, and padding this number with FYIs is how it becomes wallpaper.
		blocked := m.Waiting + m.Peers.Blocked()
		if blocked > 0 {
			systray.SetTitle(fmt.Sprintf("%d", blocked))
		} else {
			systray.SetTitle("")
		}

		// Notify only on a RISE, and never on the first pass — otherwise launching the tray would
		// announce every session that was already blocked before you started it.
		// Name WHICH session wants you — "claude · partyline" is actionable in a way that a bare count
		// isn't when three are running. Falls back to the count when more than one is waiting.
		if lastWaiting >= 0 && m.Waiting > lastWaiting {
			if m.Waiting == 1 {
				if s := firstWaiting(m.Sessions); s != nil {
					notify(fmt.Sprintf("%s · %s is waiting on you", s.Tool, s.Dir))
				} else {
					notify("1 agent is waiting on you")
				}
			} else {
				notify(fmt.Sprintf("%d agents are waiting on you", m.Waiting))
			}
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

// firstWaiting returns the session blocked on you, for a notification that can name it.
func firstWaiting(ss []session) *session {
	for i := range ss {
		if ss[i].Status == "waiting" {
			return &ss[i]
		}
	}
	return nil
}
