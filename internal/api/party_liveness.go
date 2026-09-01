package api

import (
	"net/http"
	"time"
)

// Is this party still going? — asked by anything holding a party token that outlives the
// party itself (an MCP server registered in an engine's config, say).
//
// A party token stops resolving the moment the party closes, so the obvious "does my token
// still work?" check is useless on its own: closed, never-existed and network-down all answer
// the same way. So the probe makes TWO reads against endpoints that differ in exactly one
// respect — whether they require the party to be open:
//
//	/info      party-token auth via partyByToken        → resolves ONLY while the party is open
//	/messages  party-token auth via partyByTokenForRead → resolves in ANY status (reads survive close)
//
// Live if the open-only read succeeds. Ended only when the any-status read succeeds and the
// open-only one is definitively refused — i.e. the party is there and it is not open.
// Everything else is PartyUnreachable.
//
// The asymmetry is deliberate: PartyEnded requires POSITIVE evidence, and every doubt
// (transport error, timeout, 5xx, an unrecognised token) falls to PartyUnreachable. Telling
// someone on a plane that their party ended is the same lie as telling them nothing is wrong,
// just wearing a more confident face.

// PartyLiveness is the three-state answer. The zero value is PartyUnreachable, so a
// forgotten assignment fails toward "couldn't check" rather than "ended".
type PartyLiveness int

// The three states. Declared separately rather than as one iota block so each is listed —
// and reads its own doc comment — in `go doc ./internal/api`; a caller deciding what to do
// about a party should not have to open the source to find out what the middle value means.

// PartyUnreachable — could not be checked. The host was unreachable, the read timed out, the
// server erred, or the token resolved nowhere at all. Says nothing about the party itself.
// Also the zero value, so a missed assignment cannot silently claim a party ended.
const PartyUnreachable PartyLiveness = 0

// PartyLive — the party is open and this token still opens it.
const PartyLive PartyLiveness = 1

// PartyEnded — the party exists and is no longer open. Positive evidence only.
const PartyEnded PartyLiveness = 2

func (l PartyLiveness) String() string {
	switch l {
	case PartyLive:
		return "live"
	case PartyEnded:
		return "ended"
	default:
		return "unreachable"
	}
}

// probeTimeout bounds EACH of the probe's reads. A probe runs on paths a person is waiting
// on (starting a session, listing registrations), so a hung host must cost seconds, not a
// stall. Worst case is one timeout: an unreachable open-only read short-circuits the second.
// A var, not a const, so tests can shrink it.
var probeTimeout = 4 * time.Second

// readOutcome is what one probe read tells us, which is less than the status code says:
// only "resolved", "definitively refused" and "no answer worth trusting".
type readOutcome int

const (
	readFailed   readOutcome = iota // transport error, timeout, 5xx, 408, 429 — no information
	readResolved                    // 2xx: the endpoint resolved this party
	readRefused                     // 4xx: the endpoint would not resolve this party
)

// ProbeLiveness answers whether this party is live, has ended, or could not be checked.
// Never returns an error: "could not be checked" IS one of the three answers, and a caller
// that has to interpret an error alongside a state will eventually interpret it as "ended".
func (p *PartyClient) ProbeLiveness() PartyLiveness {
	// The open-only read. A success here is the whole answer.
	switch p.probeRead("/api/v1/parties/" + p.ID + "/info") {
	case readResolved:
		return PartyLive
	case readFailed:
		// We never reached a verdict, so the any-status read cannot mean anything either —
		// its own failure would be indistinguishable from "party gone". Don't pay for it.
		return PartyUnreachable
	}

	// Refused. Either the party closed, or this token was never good. The any-status read
	// separates the two — and only its success is allowed to conclude anything.
	if p.probeRead("/api/v1/parties/"+p.ID+"/messages?limit=1") == readResolved {
		return PartyEnded
	}
	return PartyUnreachable
}

// probeRead does one bounded, party-token GET. Deliberately NOT doWithRetry: retries multiply
// the bound, and a probe that answers late is worse than one that answers "couldn't check".
func (p *PartyClient) probeRead(path string) readOutcome {
	req, err := http.NewRequest(http.MethodGet, p.Base+path, nil)
	if err != nil {
		return readFailed
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		return readFailed
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode < 300:
		return readResolved
	// Server-side trouble and back-pressure are the host's problem, not the party's status.
	case res.StatusCode >= 500, res.StatusCode == http.StatusRequestTimeout, res.StatusCode == http.StatusTooManyRequests:
		return readFailed
	default:
		return readRefused
	}
}
