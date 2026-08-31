package main

import (
	"net/url"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// A party-mcp registration outlives its party. The engine's config keeps spawning us long
// after the room closed, and the agent on the other end has no way to know: it sees twenty
// tools, calls one, and gets an auth error it reads as "partyline is broken".
//
// So when the startup probe says — positively — that the party has ended, we say so instead
// of pretending. Both MCP surfaces carry the SAME sentence, because they reach different
// readers at different moments: `instructions` is folded into the system prompt and read
// before anything is attempted, while the single remaining tool is what an agent bumps into
// mid-task. One string, rendered twice; they cannot drift.
//
// Only a positive "ended" does this. Unreachable — offline, timing out, 5xx — keeps the full
// toolset (see api.ProbeLiveness): a session that withdrew its tools because the wifi dropped
// is just a new way for partyline to look broken.

// endedProbeBudget bounds how long a request will wait on the startup probe. The probe runs
// concurrently with `initialize`, so in practice it has already answered by the time the first
// tools/list arrives; this is the backstop for a host that accepts the connection and then says
// nothing. Expiry is treated as "couldn't check" — i.e. as live.
const endedProbeBudget = 6 * time.Second

// endedServerName is the MCP server name `ptln join-mcp` registers by default, and so the name
// the removal command below names. join-mcp's --server can override it, and nothing in this
// process can see what was chosen — hence the parenthetical in the notice.
const endedServerName = "partyline-party"

// endedNotice is THE statement: the connect-time instructions and the one remaining tool both
// return exactly this. It names the party and the host so a person with several registrations
// can tell which one is stale, and prints the removal command rather than running it —
// partyline never edits an engine's config file.
//
// It must never contain the party token: this text lands in a system prompt and in transcripts.
func endedNotice(base, partyID string) string {
	var b strings.Builder
	b.WriteString("This party has ended.\n\n")
	b.WriteString("Party " + partyID + " on " + endedHost(base) + " is closed, so this MCP server has no tools " +
		"to offer — nothing here can read the channel, post, or reach the plan, and reconnecting will not " +
		"change that. Do not report this as a partyline failure or wait for access: the room is simply over.\n\n")
	b.WriteString("The registration is stale and can be removed from this engine's config:\n\n")
	b.WriteString("    claude mcp remove " + endedServerName + "\n\n")
	b.WriteString("(Registered under a different name, or in Codex / Gemini? Remove the " + endedServerName +
		" entry from that tool's MCP config — ~/.codex/config.toml or ~/.gemini/settings.json.) " +
		"partyline prints this command; it does not edit the file for you.\n\n")
	b.WriteString("The transcript of what was said is still on the party page.")
	return b.String()
}

// endedHost renders the control-plane base as the host a person would recognise, falling back
// to the raw value when it is not a URL we can parse.
func endedHost(base string) string {
	if u, err := url.Parse(strings.TrimSpace(base)); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSpace(base)
}

// endedToolDefs is the whole toolset of an ended party: one tool, whose description IS the
// notice, so a model that only reads tool descriptions gets the same statement.
func endedToolDefs(notice string) []map[string]any {
	return []map[string]any{{
		"name":        "party_ended",
		"description": notice,
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}}
}

// startLivenessProbe fires the party probe in the background, so it overlaps `initialize`
// rather than delaying it. Skipped when there is no party configured to probe — absence of an
// answer reads as live, same as unreachable.
func (s *mcpServer) startLivenessProbe() {
	if s.pc == nil || s.pc.ID == "" || s.pc.Token == "" {
		return
	}
	pc := s.pc
	ch := make(chan api.PartyLiveness, 1)
	go func() { ch <- pc.ProbeLiveness() }()
	s.liveness, s.livenessBy = ch, time.Now().Add(endedProbeBudget)
}

// partyEnded reports whether the startup probe positively answered "ended". It waits for the
// probe at most until the startup budget expires, and gives up permanently on expiry — a host
// that never answered once will not be asked to answer again mid-session.
//
// Called only from the serve loop, which is single-threaded, so these fields need no lock.
func (s *mcpServer) partyEnded() bool {
	if s.liveness != nil {
		select {
		case s.livenessState = <-s.liveness:
		case <-time.After(time.Until(s.livenessBy)):
		}
		s.liveness = nil
	}
	return s.livenessState == api.PartyEnded
}

// endedText is the notice for THIS server's party, or "" when the party is not known to have
// ended — in which case every surface behaves exactly as it always has.
func (s *mcpServer) endedText() string {
	if !s.partyEnded() {
		return ""
	}
	return endedNotice(s.pc.Base, s.pc.ID)
}
