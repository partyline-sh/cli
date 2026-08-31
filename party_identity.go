package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// Who a CLI party message reads as.
//
// The post route picks the sender prefix from the AUTH MODE, not from anything in the body:
// a party token yields `agent:<name>`, a user credential yields
// `user:<display_name || email-localpart || "member">`. Every Go caller has always posted with
// the party token, so a person typing into their own session showed up as an agent.
//
// The fix is the CREDENTIAL, not a name: send a human's post under their login token and the
// server's existing cascade does the rest. No name resolution happens here.
//
// Agents are untouched — the party runner and the agents it spawns have no login token wired in
// and keep posting with the party token.
type humanPoster struct {
	pc    *api.PartyClient
	name  string    // the @handle this session is addressed by; inert on the login-token path
	token string    // login token, or "" once we know there isn't a usable one
	out   io.Writer // stderr — party-mcp's stdout is the JSON-RPC wire
	said  bool      // the notice is per session, not per message
}

// newHumanPoster wires a poster for a session a PERSON is sitting in. token is their saved login
// token; it is ignored when the party lives on a different control plane than the one this CLI is
// logged in to, because that token would be refused anyway (credentials are per-environment —
// see internal/api/env.go).
func newHumanPoster(pc *api.PartyClient, name string, token string, out io.Writer) *humanPoster {
	if pc.Base != api.Base() {
		token = ""
	}
	return &humanPoster{pc: pc, name: name, token: strings.TrimSpace(token), out: out}
}

// post sends one message as the person. With a usable login token the message reads
// `user:<their display name>`; otherwise it still goes out under the party token (reading
// `agent:<name>`) and we say once why, naming the fix. Nothing typed is ever dropped.
func (h *humanPoster) post(body, kind string) (int64, error) {
	if h.token != "" {
		id, err := h.pc.PostAs(h.token, h.name, body, kind)
		if err == nil {
			return id, nil
		}
		var ce *api.CredentialError
		if !errors.As(err, &ce) {
			return 0, err // a real failure — don't quietly re-post under another identity
		}
		h.token = "" // refused: stop offering it for the rest of the session
	}
	h.notice()
	return h.pc.Post(h.name, body, kind)
}

// notice explains the odd-looking sender at the moment it happens, once.
func (h *humanPoster) notice() {
	if h.said || h.out == nil {
		return
	}
	h.said = true
	fmt.Fprintf(h.out, "partyline: posting as agent:%s — no usable partyline login on this machine. Run `ptln login` so your messages read as you.\n", h.name)
}
