package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// The agent-facing half of ask_session: two tools in cg-mcp that let a session address another of
// this machine's live sessions by name.
//
// Both are deliberately thin. cg-mcp is a child of the ENGINE, not of the mux, so it cannot see the
// children at all — it writes an ask to the shared store and polls for a terminal status, and the mux
// (ask_session_watch.go) does everything that touches a real PTY.

// askSessionTools are appended to the cg-mcp tool list. Descriptions carry real weight here: they are
// the only thing that teaches a model WHEN this is the right tool, and the failure they exist to
// prevent is a session asking a peer's machine (slow, cold, consent-gated) something the tab next to
// it already knows.
var askSessionTools = []map[string]any{
	{
		"name": "list_sessions",
		"description": "List the other AI sessions running right now in this ptln window, by name — e.g. \"ACR FLEET MANAGER\", \"ACR ODOO MCP\". " +
			"Call this before ask_session to get the exact name to address, and to see whether the session you want is even open.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "ask_session",
		"description": "Ask ANOTHER LIVE SESSION on this machine a question, by name, and get the answer from that actual running conversation. " +
			"Use this when the thing you need is already known to a session working on a different project — \"which port does the odoo mcp bind?\", " +
			"\"did you change the auth header format?\". The answer comes from its WARM CONTEXT, so it can tell you things no fresh process could reconstruct. " +
			"Prefer this over ask_peer for anything on this machine: ask_peer reaches a teammate's box, needs their consent, and answers from a cold session with no history. " +
			"The target is asked to answer, not to act. It may be mid-turn, in which case this waits briefly and then tells you it's busy.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":  map[string]any{"type": "string", "description": "the session's name as shown by list_sessions. A partial name works if it matches exactly one."},
				"question": map[string]any{"type": "string", "description": "what to ask. Include the context it needs — it sees this question, not your conversation."},
			},
			"required": []string{"session", "question"},
		},
	},
}

// askSessionPoll is how often the tool re-reads the store while waiting. The mux writes a terminal
// status the moment it has one, so this is latency-on-completion, not a duty cycle.
const askSessionPoll = 500 * time.Millisecond

func (s *cgServer) handleListSessions(enc *json.Encoder, req rpcReq) {
	self := strings.TrimSpace(os.Getenv("PARTYLINE_SESSION_KEY"))
	if self == "" {
		s.toolResult(enc, req.ID, "This session isn't running inside a ptln window, so there are no sibling sessions to list. "+
			"ask_session only works between sessions in the same `ptln` mux.", true)
		return
	}
	// The mux owns the child list; cg-mcp can't see it. The store is the seam, so the mux publishes
	// the roster there for exactly this read.
	live := publishedSessions()
	out := make([]string, 0, len(live))
	for _, c := range live {
		if c.Key == self {
			continue // listing yourself invites asking yourself, which deadlocks
		}
		out = append(out, "· "+c.Label)
	}
	if len(out) == 0 {
		s.toolResult(enc, req.ID, "No other sessions are open in this ptln window right now.", false)
		return
	}
	s.toolResult(enc, req.ID, "Live sessions you can ask (use the name exactly):\n"+strings.Join(out, "\n"), false)
}

func (s *cgServer) handleAskSession(enc *json.Encoder, req rpcReq) {
	self := strings.TrimSpace(os.Getenv("PARTYLINE_SESSION_KEY"))
	if self == "" {
		s.toolResult(enc, req.ID, "This session isn't running inside a ptln window, so it has no siblings to ask. "+
			"Use ask_peer to reach a teammate's machine instead.", true)
		return
	}
	var p struct {
		Args struct {
			Session  string `json:"session"`
			Question string `json:"question"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	target := strings.TrimSpace(p.Args.Session)
	question := strings.TrimSpace(p.Args.Question)
	if target == "" || question == "" {
		s.toolResult(enc, req.ID, "ask_session needs session (a name from list_sessions) and question.", true)
		return
	}
	// Same bound, same reason as ask_peer: the question is going into another agent's prompt, and a
	// 40,000-character paste is a denial of service on a human's terminal as much as on a model.
	if over := questionTooLongNote(question); over != "" {
		s.toolResult(enc, req.ID, "ask_session: "+over+". Nothing was sent. Ask something shorter, or split it.", true)
		return
	}

	a := sessionAsk{
		ID:        newAskID(),
		From:      self,
		FromLabel: strings.TrimSpace(os.Getenv("PARTYLINE_SESSION_LABEL")),
		Target:    target,
		Question:  question,
		Status:    askOpen,
		AskedAt:   time.Now(),
	}
	putAsk(a)

	// Block until the mux reaches a terminal state. Blocking (rather than returning an id to poll,
	// the way ask_peer must) is right here: the answer is seconds away because the session is on this
	// machine and already warm — handing back a ticket would make the model manage a wait it doesn't
	// need to. askTTL bounds it, and the store's own pruning guarantees a terminal status by then.
	deadline := time.Now().Add(askTTL + 5*time.Second)
	for time.Now().Before(deadline) {
		got, ok := getAsk(a.ID)
		if !ok {
			s.toolResult(enc, req.ID, "ask_session: the question was dropped before it could be delivered. Is `ptln` still running?", true)
			return
		}
		switch got.Status {
		case askAnswered:
			s.toolResult(enc, req.ID, "\""+got.Target+"\" says:\n\n"+got.Answer, false)
			return
		case askFailed:
			s.toolResult(enc, req.ID, "ask_session: "+got.Reason, true)
			return
		}
		time.Sleep(askSessionPoll)
	}
	s.toolResult(enc, req.ID, "ask_session: no answer within "+askTTL.String()+". The session may be working on something long — try again, or switch to it and look.", true)
}
