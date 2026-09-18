package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptysess"
	"partyline.sh/partyline/internal/relay"
	"partyline.sh/partyline/internal/wormhole"
)

// Empty: the relay comes from the instance you are pointed at, not from a hostname compiled in
// here. pppp.sh is retired.
const defaultShareRelay = ""

// shareInfo tracks a live share so the menu can show the link + stop it. Keyed by the session
// pointer (a mux child), guarded by shareMu.
type shareInfo struct {
	link string
	stop func()
}

var (
	shareMu sync.Mutex
	shares  = map[*ptysess.Session]*shareInfo{}
)

// shareMenu is the `ctrl-\ s` overlay (run in a cooked terminal via the mux's suspend, so the join
// link is selectable). It shares the FOCUSED session as a live, view-only broadcast over the same
// E2EE relay `ptln start` uses, or opens a fresh shared shell in a new tab. Letting a viewer type
// is a follow-up (the mux repurposed the session's own ctrl-\ grant keybinding).
func shareMenu(mx shareMenuTarget) {
	in := stdin()
	sess, label := mx.ActiveShareable()

	fmt.Print("\x1b[2J\x1b[H")
	fmt.Println("  \x1b[1m☎  Share\x1b[0m")
	fmt.Println("  ─────────────────────────────────────────────")

	shareMu.Lock()
	info := shares[sess]
	shareMu.Unlock()

	if sess != nil && info != nil { // already sharing this session
		fmt.Printf("  sharing: %s  %s\n", label, dim("(live · view-only)"))
		fmt.Printf("  link:  %s\n\n", info.link)
		fmt.Println("    s) stop sharing this session")
		fmt.Println("    t) open a new shared terminal tab")
		cgHintPrint("SHARE · LIVE")
		switch string(menuKey()) { // single keypress; q / esc / enter cancel
		case "s":
			// Stopping invalidates a link other people may be watching right now, so it asks first.
			if yes, ok := Confirm("stop sharing — the link stops working for everyone. sure?", false); !ok || !yes {
				return
			}
			info.stop()
			shareMu.Lock()
			delete(shares, sess)
			shareMu.Unlock()
			mx.SetActiveShared(false) // clear the tab's live-share marker
			fmt.Println("\n  ✓ stopped — that link no longer works.")
			pause(in)
		case "t":
			fmt.Println("\n  " + openSharedTerminalTab())
			pause(in)
		}
		return
	}

	if sess != nil {
		fmt.Printf("    1) share this session: %s  %s\n", label, dim("(live · view-only)"))
	} else {
		fmt.Printf("  %s\n", dim("(no focused session — switch to one to share it)"))
	}
	fmt.Println("    2) open a new shared terminal tab")
	cgHintPrint("SHARE")
	switch string(menuKey()) { // single keypress; q / esc / enter cancel
	case "1":
		if sess == nil {
			return
		}
		fmt.Println("\n  starting share…")
		link, stop, err := startSessionShare(sess)
		if err != nil {
			fmt.Printf("  ✗ couldn't start sharing: %v\n", err)
			pause(in)
			return
		}
		shareMu.Lock()
		shares[sess] = &shareInfo{link: link, stop: stop}
		shareMu.Unlock()
		mx.SetActiveShared(true) // light the tab's live-share marker
		// Drop the entry when the session ends on its own (the serve loop already exits on Done).
		go func() { <-sess.Done; shareMu.Lock(); delete(shares, sess); shareMu.Unlock() }()
		fmt.Printf("  ✓ live (view-only) — share this link:\n\n    %s\n\n", link)
		fmt.Printf("  %s\n", dim("joiners run:  ptln join '<link>'  · they watch; typing stays yours."))
		fmt.Printf("  %s\n", dim("ctrl-\\ s again to stop sharing."))
		pause(in)
	case "2":
		fmt.Println("\n  " + openSharedTerminalTab())
		pause(in)
	}
}

// startSessionShare registers a session server-side, dials the blind relay, and serves the given
// (already-running) mux child to joiners over the E2EE channel — view-only, since the session was
// spawned non-open. Returns the join link and a stop func that tears down serving WITHOUT killing
// the session (the child keeps running locally). Reuses the exact `ptln start` transport.
func startSessionShare(sess *ptysess.Session) (string, func(), error) {
	apic := api.New()
	if apic.Token == "" {
		return "", nil, fmt.Errorf("not logged in — run `ptln login`")
	}
	key := make([]byte, wormhole.KeyLen)
	if _, err := rand.Read(key); err != nil {
		return "", nil, err
	}
	keyB64 := base64.RawURLEncoding.EncodeToString(key)

	relayAddr := defaultShareRelay
	rh, rp, _ := strings.Cut(relayAddr, ":")
	rport, _ := strconv.Atoi(rp)
	reg, err := apic.RegisterSession([]api.Endpoint{{Type: "relay", Addr: rh, Port: rport}}, "org", "", keyB64, false)
	if err != nil {
		return "", nil, err
	}
	if reg.Relay != "" { // control-plane-assigned pool relay
		relayAddr = reg.Relay
	}

	ms, err := relay.DialHostE2EE(relayAddr, reg.JoinCode, apic.Token)
	if err != nil {
		return "", nil, err
	}
	stop := make(chan struct{})
	go relay.ServeHostReconnectingStop(sess, ms, key, reg.JoinCode, relayAddr, apic.Token, false, stop)

	link := fmt.Sprintf("%s/j/%s#k=%s&r=%s", api.Base(), reg.JoinCode, keyB64, relayAddr)
	var once sync.Once
	stopFn := func() {
		once.Do(func() {
			close(stop)
			apic.EndSession(reg.ID, sess.DriverCount())
		})
	}
	return link, stopFn, nil
}

// openSharedTerminalTab opens a NEW terminal tab/window running `ptln start` — a fresh shared
// shell (its own join link prints in that tab). macOS iTerm2/Apple Terminal are automated via
// osascript; anything else gets a copy-paste instruction. Returns a one-line status.
func openSharedTerminalTab() string {
	exe := selfExe()
	dir, _ := os.Getwd()
	shellCmd := fmt.Sprintf("cd %s && %s start", shQuote(dir), shQuote(exe))

	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app":
		script := fmt.Sprintf(`tell application "iTerm"
  tell current window
    create tab with default profile
    tell current session to write text %s
  end tell
end tell`, asQuote(shellCmd))
		if err := runOsascript(script); err != nil {
			return "✗ couldn't open an iTerm tab — run `ptln start` in a new tab yourself"
		}
		return "✓ opened a shared terminal in a new iTerm tab (its join link is printing there)"
	case "Apple_Terminal":
		script := fmt.Sprintf(`tell application "Terminal"
  activate
  do script %s
end tell`, asQuote(shellCmd))
		if err := runOsascript(script); err != nil {
			return "✗ couldn't open a Terminal window — run `ptln start` in a new tab yourself"
		}
		return "✓ opened a shared terminal in a new Terminal window (its join link is printing there)"
	default:
		return "open a new tab in your terminal and run `ptln start` to share a shell"
	}
}

func runOsascript(script string) error {
	return exec.Command("osascript", "-e", script).Run()
}
