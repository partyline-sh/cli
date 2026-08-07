package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/wormhole"
)

// joinStream holds the joiner's current relay stream. Writes (stdin keystrokes,
// resize) go through it and survive reconnects: the long-lived stdin/SIGWINCH
// goroutines write to whatever stream is current, and a write during a reconnect
// gap just no-ops. Only the main loop calls set()/ReadFrame; writes are
// mutex-guarded against set() and against each other (WriteFrame is itself safe).
type joinStream struct {
	mu sync.Mutex
	st *wormhole.Stream
}

func (j *joinStream) set(st *wormhole.Stream) { j.mu.Lock(); j.st = st; j.mu.Unlock() }
func (j *joinStream) get() *wormhole.Stream   { j.mu.Lock(); defer j.mu.Unlock(); return j.st }
func (j *joinStream) write(t wormhole.FrameType, p []byte) {
	if st := j.get(); st != nil {
		_ = st.WriteFrame(t, p)
	}
}

// dialJoin connects to the relay for `code`, performs the Noise handshake with key,
// and returns a ready stream. A non-nil error means we couldn't connect (relay
// unreachable, or no live session for the code — the relay reports no host).
func dialJoin(relayAddr, code string, key []byte) (*wormhole.Stream, error) {
	c, err := net.DialTimeout("tcp", relayAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("can't reach the relay (%s): %w", relayAddr, err)
	}
	ctrl, _ := json.Marshal(wormhole.JoinCtrl{Code: code})
	if err := wormhole.WriteHeader(c, wormhole.RoleJoiner, ctrl); err != nil {
		c.Close()
		return nil, err
	}
	ok, err := wormhole.ReadAck(c)
	if err != nil || !ok {
		c.Close()
		return nil, fmt.Errorf("no live session for that code (it may have ended)")
	}
	conn, err := wormhole.Initiator(c, key)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("encrypted handshake failed — is the link complete/current? (%w)", err)
	}
	return wormhole.NewStream(conn), nil
}

// parseJoinLink accepts the full link, the path, or the bare code+fragment and
// returns the routing code, the 32-byte encryption key, and the relay endpoint.
// The key and relay live ONLY in the URL fragment (`#k=…&r=…`) — never sent to a
// server — so anonymous joiners dial the right relay with no lookup, and a link
// without a key can't be joined (E2EE-only). relay is "" for older links; the
// caller falls back to its default.
func parseJoinLink(arg string) (code string, key []byte, relay string, err error) {
	i := strings.Index(arg, "#")
	if i < 0 {
		return "", nil, "", fmt.Errorf("that looks like a bare code — paste the full link in quotes (it carries the encryption key, and the & in it is a shell character):\n  ptln join 'https://partyline.sh/j/<code>#k=<key>&r=<relay>'")
	}
	left, frag := strings.TrimRight(arg[:i], "/"), arg[i+1:]
	if j := strings.LastIndex(left, "/"); j >= 0 {
		code = left[j+1:]
	} else {
		code = left
	}
	var k string
	for _, kv := range strings.Split(frag, "&") {
		switch {
		case strings.HasPrefix(kv, "k="):
			k = kv[2:]
		case strings.HasPrefix(kv, "r="):
			relay = kv[2:]
		}
	}
	if code == "" || k == "" {
		return "", nil, "", fmt.Errorf("malformed join link")
	}
	key, err = base64.RawURLEncoding.DecodeString(k)
	if err != nil || len(key) != wormhole.KeyLen {
		return "", nil, "", fmt.Errorf("bad key in link")
	}
	return code, key, relay, nil
}

// joinE2EE connects to the relay, performs the Noise handshake with the link key,
// and runs the joiner terminal loop. The relay only sees ciphertext. If the relay
// connection drops mid-session (wifi blip, relay restart, host reconnecting), the
// joiner re-dials with the same code+key and resumes — the host repaints the
// current screen on re-attach. A graceful end (host /pexit) arrives as FrameClose
// and exits immediately; only a transport error triggers a reconnect.
// identFn returns the identity to present on each (re)connect — re-minted each
// time so a logged-in joiner's signed assertion (which is short-lived) is fresh
// after a long reconnect, rather than expiring mid-session on an invite-only line.
func joinE2EE(relayAddr, code string, key []byte, identFn func() string) error {
	fmt.Printf("☎ joining %s …\r\n", code)
	st, err := dialJoin(relayAddr, code, key)
	if err != nil {
		return err // initial connect failure: surface it (bad/expired link, relay down)
	}

	// raw terminal so keystrokes pass straight through (like ssh)
	fd := int(os.Stdin.Fd())
	restoreTerm := func() {}
	if old, e := term.MakeRaw(fd); e == nil {
		restoreTerm = func() { _ = term.Restore(fd, old) }
		defer term.Restore(fd, old)
	}
	// On ANY exit — clean close, host end, OR a dead connection that never relays the
	// host program's own reset sequences — put the local terminal back to a sane
	// state. A mirrored full-screen TUI (an agent, vim, etc.) turns on mouse reporting
	// and the alternate screen on the VIEWER; without this, a dropped session leaves
	// the shell spewing mouse escape codes ("command not found: 33M…") and beeping.
	// termios restore alone does NOT undo these DEC private modes.
	defer sanitizeTerminal()
	// …and the same on a SIGINT/SIGTERM. Both defers above are skipped when a signal takes
	// its default disposition, which is exactly how a joiner ends up in a shell spewing mouse
	// reports. Raw mode swallows a keyboard ctrl-C, so this only fires while the terminal is
	// cooked (a blocking dial/reconnect) or on a real kill — no normal quit path changes.
	// Both steps are idempotent, so racing the deferred pair above is harmless.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		restoreTerm()
		sanitizeTerminal()
		os.Exit(130)
	}()
	curSize := func() (int, int) {
		if w, h, e := term.GetSize(fd); e == nil && w > 0 {
			return w, h
		}
		return 80, 24
	}

	hold := &joinStream{st: st}
	handshake := func() {
		w, h := curSize()
		hold.write(wormhole.FrameIdentity, []byte(identFn()))
		hold.write(wormhole.FrameResize, wormhole.EncodeResize(w, h))
	}
	handshake()

	// resize on SIGWINCH → current stream (survives reconnects)
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGWINCH)
		for range ch {
			w, h := curSize()
			hold.write(wormhole.FrameResize, wormhole.EncodeResize(w, h))
		}
	}()

	// Local command palette: ctrl-\ pops an arrow-navigable menu (rendered here,
	// not host-side) whose choices are sent as the host's existing ctrl-\<letter>
	// chords. menu.out gates stdout so host output never paints over the menu.
	menu := &joinMenu{size: curSize}
	menu.repaint = func() { // ask the host for a full snapshot to repaint after the menu closes
		hold.write(wormhole.FrameRepaint, nil)
	}

	// stdin → input frames on the current stream. On stdin EOF (non-interactive /
	// watch-only joiner) we just stop reading — we do NOT tear down. A write during
	// a reconnect gap no-ops; we keep reading so input resumes once reconnected.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, e := os.Stdin.Read(buf)
			if n > 0 {
				menu.feed(buf[:n], hold.write) // intercepts ctrl-\, forwards the rest
			}
			if e != nil {
				return
			}
		}
	}()

	// output frames → stdout, reconnecting transparently if the relay drops.
	for {
		t, payload, rerr := hold.get().ReadFrame()
		if rerr == nil {
			switch t {
			case wormhole.FrameOutput:
				menu.out(payload) // buffered while the menu is open, else straight to stdout
			case wormhole.FrameClose:
				fmt.Print("\r\n☎ partyline session closed\r\n")
				return nil
			}
			continue
		}
		// Transport error, NOT a graceful close — the host may be reconnecting too.
		// Re-dial with backoff, bounded by the control-plane reap window (a host
		// that never returns is reaped after ~3m, so there's nothing to rejoin).
		fmt.Print("\r\n☎ connection lost — reconnecting…\r\n")
		backoff := time.Second
		deadline := time.Now().Add(3 * time.Minute)
		for {
			if ns, derr := dialJoin(relayAddr, code, key); derr == nil {
				hold.set(ns)
				handshake()
				fmt.Print("\r\n☎ reconnected — you're back on the line\r\n")
				break
			}
			if time.Now().After(deadline) {
				fmt.Print("\r\n☎ lost the connection and couldn't reconnect after 3 minutes — this session has ended or is unreachable.\r\n" +
					"  Ask the host for a new link, or have them run `ptln start` to begin a fresh session.\r\n")
				return nil
			}
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				if backoff *= 2; backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
	}
}
