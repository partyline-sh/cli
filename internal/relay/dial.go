// Package relay (host side): dial the blind relay and serve each joiner over an
// end-to-end-encrypted channel. The relay only ever splices ciphertext — the
// Noise handshake + framed terminal I/O run host↔joiner, keyed by the session
// key that lives only in the join link.
package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/yamux"

	"partyline.sh/partyline/internal/identity"
	"partyline.sh/partyline/internal/obs"
	"partyline.sh/partyline/internal/ptysess"
	"partyline.sh/partyline/internal/wormhole"
)

// DialHostE2EE dials the relay and registers the code (ownership verified server-
// side from the token). It's fast — call it synchronously so the CLI can show
// connect status — then hand the returned session to ServeHostE2EE in a goroutine.
func DialHostE2EE(relayAddr, code, token string) (*yamux.Session, error) {
	c, err := net.DialTimeout("tcp", relayAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("relay dial: %w", err)
	}
	ctrl, _ := json.Marshal(wormhole.HostCtrl{Code: code, Token: token})
	if err := wormhole.WriteHeader(c, wormhole.RoleHost, ctrl); err != nil {
		c.Close()
		return nil, err
	}
	ok, err := wormhole.ReadAck(c)
	if err != nil || !ok {
		c.Close()
		return nil, fmt.Errorf("relay register rejected (ownership check) — are you logged in?")
	}
	ycfg := yamux.DefaultConfig()
	ycfg.EnableKeepAlive = true
	ycfg.LogOutput = io.Discard
	ms, err := yamux.Server(c, ycfg) // relay opens streams; host accepts
	if err != nil {
		c.Close()
		return nil, err
	}
	return ms, nil
}

// ServeHostReconnecting serves joiners over the relay and transparently re-dials if
// the connection drops (network blip, relay restart, laptop sleep). The session and
// Noise key are unchanged across reconnects, so joiners reconnect with their
// existing link — a transport reset no longer orphans a live session. It serves the
// initial session `ms`, then loops: on a drop, re-dial with capped backoff and
// resume. Returns only when the local session ends (sess.Done) — the heartbeat loop
// closes that if the session is ended/reaped server-side, so this never spins
// forever on a session that's truly gone.
func ServeHostReconnecting(sess *ptysess.Session, ms *yamux.Session, key []byte, code, relayAddr, token string, inviteOnly bool) {
	defer obs.Guard("relayServe")
	const maxBackoff = 30 * time.Second
	backoff := time.Second
	for {
		serveErr := ServeHostE2EE(sess, ms, key, code, inviteOnly)
		// Local session ended (host exited, or reaped/ended server-side) → stop.
		select {
		case <-sess.Done:
			return
		default:
		}
		fmt.Printf("\r\n[partyline] relay connection lost (%v) — reconnecting…\r\n", serveErr)

		// Re-dial with backoff, keeping the same code+key; bail if the local session
		// ends while we wait. Bounded to ~3 min (the control-plane reap window): past
		// that the session is gone server-side, so we stop trying and tell the host —
		// but leave their shell running so they don't lose their terminal state.
		deadline := time.Now().Add(3 * time.Minute)
		for {
			select {
			case <-sess.Done:
				return
			case <-time.After(backoff):
			}
			next, derr := DialHostE2EE(relayAddr, code, token)
			if derr == nil {
				ms = next
				backoff = time.Second
				fmt.Print("\r\n[partyline] relay reconnected — your link is live again\r\n")
				break
			}
			if time.Now().After(deadline) {
				fmt.Print("\r\n☎ relay connection lost — couldn't reconnect after 3 minutes.\r\n" +
					"  Your shell is still running here, but no one can join over the link anymore.\r\n" +
					"  Press ctrl-\\ then q (or type /pexit) to close it, then run `ptln start` for a fresh session.\r\n")
				return
			}
			if backoff < maxBackoff {
				if backoff *= 2; backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	}
}

// ServeHostE2EE accepts joiner streams on the relay session and serves each over
// the end-to-end-encrypted channel. Blocks until the session closes. key is the
// 32-byte session PSK; inviteOnly rejects joiners without a valid identity assertion.
func ServeHostE2EE(sess *ptysess.Session, ms *yamux.Session, key []byte, code string, inviteOnly bool) error {
	defer ms.Close()
	for {
		stream, err := ms.AcceptStream()
		if err != nil {
			return err // session closed (relay/host gone)
		}
		go serveJoinerE2EE(sess, stream, key, code, inviteOnly)
	}
}

func serveJoinerE2EE(sess *ptysess.Session, stream net.Conn, key []byte, code string, inviteOnly bool) {
	defer obs.Guard("serveJoiner") // a malformed/hostile joiner must never crash the host
	defer stream.Close()
	conn, err := wormhole.Responder(stream, key)
	if err != nil {
		return // wrong key / garbage — drop (relay can't produce a valid handshake)
	}
	st := wormhole.NewStream(conn)

	// Identity + initial size arrive INSIDE the encrypted channel, so the relay
	// never sees the joiner's name or terminal dimensions. FrameIdentity carries
	// either a signed partyline assertion (→ verified handle) or a plain name.
	name, cols, rows := "guest", 80, 24
	verified, full := false, false
	if t, p, err := st.ReadFrame(); err == nil && t == wormhole.FrameIdentity && len(p) > 0 {
		raw := string(p)
		if identity.LooksLikeAssertion(raw) {
			if claims, verr := identity.Verify(raw, code); verr == nil {
				name = "✓" + claims.Sub
				verified = true
				full = claims.Access == "full" // viewer vs full-access (driving wall)
			}
			// assertion present but invalid → stay an unverified guest
		} else {
			name = sanitizeName(raw)
		}
	} else if err != nil {
		return
	}
	if inviteOnly && !verified {
		_ = st.WriteFrame(wormhole.FrameOutput,
			[]byte("\r\npartyline: this session needs a partyline account — sign in and get invited to join.\r\n"))
		return
	}
	if sess.Locked() {
		_ = st.WriteFrame(wormhole.FrameOutput,
			[]byte("\r\npartyline: the host has locked this session to new joiners.\r\n"))
		return
	}
	if t, p, err := st.ReadFrame(); err == nil && t == wormhole.FrameResize {
		if cc, rr, ok := wormhole.DecodeResize(p); ok && cc > 0 && rr > 0 {
			cols, rows = cc, rr
		}
	} else if err != nil {
		return
	}

	p := sess.Attach(name, st.OutputWriter(), cols, rows, false, full)
	defer sess.Detach(p)
	// Tell the joiner explicitly when the session ends gracefully (host /pexit or
	// shell exit), so it stops cleanly instead of treating it as a transport drop
	// and reconnecting. A transport drop produces a read error with NO FrameClose,
	// which is exactly how the joiner distinguishes "ended" from "reconnect".
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-sess.Done:
			_ = st.WriteFrame(wormhole.FrameClose, nil) // WriteFrame is mutex-safe
		case <-stop:
		}
	}()
	for {
		t, payload, err := st.ReadFrame()
		if err != nil {
			return
		}
		switch t {
		case wormhole.FrameInput:
			if !sess.HandleInput(p, payload) {
				// HandleInput returns false only on an intentional leave (ctrl-\ d
				// or /pexit). Tell the joiner it's a graceful close so it exits
				// instead of seeing a dead stream and auto-reconnecting.
				_ = st.WriteFrame(wormhole.FrameClose, nil)
				return
			}
		case wormhole.FrameResize:
			if cc, rr, ok := wormhole.DecodeResize(payload); ok && cc > 0 && rr > 0 {
				sess.Resize(p, cc, rr)
			}
		case wormhole.FrameRepaint:
			// The joiner showed a local overlay (its command menu) and needs the
			// screen back. Re-send a full snapshot — works over a full-screen app
			// (vim/claude) where relying on the program to repaint is unreliable.
			_ = st.WriteFrame(wormhole.FrameOutput, sess.Snapshot())
		}
	}
}

// sanitizeName keeps a joiner-supplied name printable and bounded.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f && b.Len() < 32 {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "guest"
	}
	return b.String()
}
