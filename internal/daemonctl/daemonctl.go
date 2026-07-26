// Package daemonctl is the LOCAL control channel to this machine's partyline daemon: a small
// request/response contract any same-host process can speak.
//
// WHY IT EXISTS. Answering a peer's consult structurally requires the daemon process, for two
// independent reasons: (1) the answer route is DEVICE-token authed and the write is scoped to
// target_daemon, and no other process holds that token; (2) the answer is produced by a read-only
// engine turn against this machine's OWN checkout, resolved through the daemon's local registry —
// deliberately, because the daemon is the final authority on what it will answer, never the control
// plane. So a UI that wants to answer a question can't do it itself; it has to ask the daemon.
//
// WHY UNIX, NOT TCP. There is no remote caller for this, ever: the daemon and its UIs are the same
// user on the same box. A unix socket at a 0700 path with 0600 perms is authenticated by the
// filesystem — no port to firewall, nothing reachable off the machine, no token to hand out (and so
// none to leak). The payload carries a consult id and an action; never a token, never a path.
//
// SECOND CLIENTS ARE EXPECTED. The `ctrl-\ p` modal is the first caller; ptln-tray (a separate
// binary, so it can't share package main) is the next. Hence the typed wire in its own package, and
// hence the rule that VALIDATION LIVES ON THE SERVER SIDE — a check implemented in one client's code
// path would be bypassed by the other. The daemon re-validates every request, for every caller.
package daemonctl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Version is the wire version. A client sends it; the daemon refuses anything it doesn't know, so an
// older daemon meeting a newer client fails loudly instead of half-understanding a request.
const Version = 1

// The operations. Fetch and DECIDE are deliberately separate calls: a one-click "approve" in a menu is
// how people approve things they never read, so the contract makes surfacing the question the obvious
// first step — and ApproveConsult additionally has to echo a digest of the question text it showed
// (see Shown), which a caller can only produce by having fetched it.
const (
	OpPing           = "ping"            // is a daemon listening, and which one
	OpConsults       = "consults"        // list the questions waiting on this machine
	OpConsult        = "consult"         // one question in full — what you show the human
	OpApproveConsult = "approve-consult" // answer it read-only (requires Shown)
	OpDenyConsult    = "deny-consult"    // decline it, freeing the asker now
)

// ErrNoDaemon means nothing is listening on the socket — no daemon is running on this machine.
// Callers should say so plainly ("start `ptln daemon`"), not report a generic failure.
var ErrNoDaemon = errors.New("no partyline daemon is listening on this machine")

// Request is one command. It names a consult and an action; it carries NO secret.
type Request struct {
	V  int    `json:"v"`
	Op string `json:"op"`
	ID string `json:"id,omitempty"` // consult id — a handle, meaningless without the daemon's token
	// Shown is the digest of the question text the caller displayed to the human, required by
	// OpApproveConsult. It is not authentication (a local caller is already trusted) — it is a
	// PROOF-OF-SURFACING: you cannot compute it without having fetched the question, so "approve
	// without ever showing it" isn't an available shortcut. The daemon recomputes and compares.
	Shown  string `json:"shown,omitempty"`
	Reason string `json:"reason,omitempty"` // decline note, shown to the asker
}

// Consult is one pending question as a local UI needs it: who/what it's about, the text to show, and
// how long it has been waiting.
type Consult struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Question   string `json:"question"`
	WaitingSec int    `json:"waiting_sec"`
}

// Response is one reply. OK plus at most one payload; Error is a human sentence, safe to print.
type Response struct {
	V        int        `json:"v"`
	OK       bool       `json:"ok"`
	Error    string     `json:"error,omitempty"`
	DaemonID string     `json:"daemon_id,omitempty"` // which daemon answered (already public in `ptln state`)
	Consults []Consult  `json:"consults,omitempty"`
	Consult  *Consult   `json:"consult,omitempty"`
	Started  bool       `json:"started,omitempty"` // an answer turn is now running
	At       *time.Time `json:"at,omitempty"`      // reserved: when the daemon acted
}

// SocketPath is where the daemon listens: inside the 0700 state dir, next to the device token.
func SocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "daemon", "control.sock")
}

// QuestionDigest is the proof-of-surfacing digest: a truncated sha256 of the exact question text.
// Not a secret and not a MAC — just a value you can only produce if you hold the question.
func QuestionDigest(question string) string {
	sum := sha256.Sum256([]byte(question))
	return hex.EncodeToString(sum[:8])
}

const dialTimeout = 2 * time.Second
const ioTimeout = 5 * time.Second

// Client is a handle on one daemon's control socket. Local() is the one every real caller wants; the
// explicit Path exists so a test (or a future non-default state dir) can point somewhere else.
type Client struct{ Path string }

// Local is the daemon on this machine.
func Local() Client { return Client{Path: SocketPath()} }

// Do sends one request and reads one response. One connection per command: there is no session, so
// there is nothing to resynchronise after an error.
func (c Client) Do(req Request) (Response, error) {
	req.V = Version
	conn, err := net.DialTimeout("unix", c.Path, dialTimeout)
	if err != nil {
		return Response{}, ErrNoDaemon
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var res Response
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return Response{}, err
	}
	if !res.OK {
		if res.Error == "" {
			res.Error = "the daemon refused that"
		}
		return res, errors.New(res.Error)
	}
	return res, nil
}

// Ping returns the id of the daemon listening here. Use it to tell "no daemon running" apart from
// "this is the wrong machine for that consult".
func (c Client) Ping() (string, error) {
	res, err := c.Do(Request{Op: OpPing})
	return res.DaemonID, err
}

// ListConsults lists the questions waiting on this machine — the tray's row count, and any surface
// that has no account token (this needs none: the daemon already holds them).
func (c Client) ListConsults() ([]Consult, error) {
	res, err := c.Do(Request{Op: OpConsults})
	return res.Consults, err
}

// GetConsult fetches one question in full. Call this and SHOW it before approving.
func (c Client) GetConsult(id string) (*Consult, error) {
	res, err := c.Do(Request{Op: OpConsult, ID: id})
	if err != nil {
		return nil, err
	}
	if res.Consult == nil {
		return nil, fmt.Errorf("consult %s is not waiting on this machine", id)
	}
	return res.Consult, nil
}

// ApproveConsult asks the daemon to answer a consult with one read-only engine turn. shownQuestion is
// the question text the caller actually displayed — passing it is what proves the human saw it.
func (c Client) ApproveConsult(id, shownQuestion string) error {
	_, err := c.Do(Request{Op: OpApproveConsult, ID: id, Shown: QuestionDigest(shownQuestion)})
	return err
}

// DenyConsult declines a consult, freeing the asker immediately rather than at the timeout. No digest:
// declining unread is the conservative answer, and making it harder would push people toward approving.
func (c Client) DenyConsult(id, reason string) error {
	_, err := c.Do(Request{Op: OpDenyConsult, ID: id, Reason: reason})
	return err
}

// Serve listens on path and answers each request with handle. Returns a stop func. The socket is
// chmod 0600 immediately after bind (umask can't be relied on) and removed on stop; a stale file from
// a killed process is replaced, since a socket nobody is listening on can't be connected to anyway.
//
// handle runs the DAEMON's validation. Every rule — is this consult really addressed to us, was the
// question actually shown — belongs there, not in a client.
func Serve(path string, handle func(Request) Response) (stop func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go serveConn(conn, handle)
		}
	}()
	return func() {
		_ = ln.Close()
		_ = os.Remove(path)
	}, nil
}

func serveConn(conn net.Conn, handle func(Request) Response) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{V: Version, Error: "malformed request"})
		return
	}
	res := Response{}
	if req.V != Version {
		res.Error = fmt.Sprintf("unsupported control protocol v%d (this daemon speaks v%d) — update ptln", req.V, Version)
	} else {
		res = handle(req)
	}
	res.V = Version
	_ = json.NewEncoder(conn).Encode(res)
}
