package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// state.go — `ptln state`, ONE machine-readable snapshot of everything this machine is doing.
//
// The tray polls this. It's a superset of `ptln daemon state` because the tray shows more than the
// daemon: whether you're signed in, and which AI sessions are live — in particular which ones are
// WAITING ON YOU, the state that costs real wall-clock and is invisible when the terminal is buried.
//
// The tray reimplements nothing: one exec, one JSON object, all logic staying in the CLI.
//
// EMITS NO SECRETS and NO ABSOLUTE PATHS beyond the session cwd, which is the user's own directory
// shown back to them on their own machine (the no-path invariant protects the CONTROL PLANE from
// learning local paths — this never leaves the box).

type accountState struct {
	LoggedIn bool   `json:"logged_in"`
	Email    string `json:"email,omitempty"`
}

// sessionState is one live AI session as the tray shows it: enough to identify and act on, never the
// conversation itself. STATE AND CONTROL, NEVER CONTENT — the O.13 line.
type sessionState struct {
	ID     string `json:"id"`
	Tool   string `json:"tool"`             // claude | codex | gemini | …
	Dir    string `json:"dir,omitempty"`    // working directory basename
	Title  string `json:"title,omitempty"`  // first user message, already trimmed
	Status string `json:"status,omitempty"` // "waiting" (your move) | "active" (working)
}

// rateLimitState mirrors the local breadcrumb crank leaves when a provider refuses work. Present
// only while it's still relevant — see readRateLimitNote for the staleness rules.
type rateLimitState struct {
	ResetAt string `json:"reset_at,omitempty"` // RFC3339; empty = none given (needs a human, not a wait)
	Note    string `json:"note,omitempty"`
	Run     string `json:"run,omitempty"`
}

// peerConsultState is ONE question a teammate's agent is waiting on this machine's owner to approve.
//
// THIS CARRIES QUESTION TEXT, AND THAT IS A REVERSAL. The first design of this field was counts-only,
// reasoning that a 4s-polled subprocess is no place for a teammate's words and that the tray's job is
// "something needs you" plus routing. The reversal is the surfacing invariant, not convenience:
// approving a consult requires echoing a digest of the question text that was DISPLAYED
// (daemonctl.QuestionDigest), precisely so nobody can approve what they never read. If the tray shows
// only a count, the only way to approve is to open a terminal — and the owner's requirement is one
// click. Putting the question in the menu is what makes the click honest: reading it in the submenu IS
// the surfacing the digest attests to. Counts-only would have forced either a terminal detour or a
// weakened digest check, and the digest check is not negotiable.
//
// So the bound is kept tight instead: INBOUND ONLY (never an answer, never an outbound question), hard
// truncation, and a small row cap. This is the one exception to the state/never-content line, and it
// exists to protect a security check, not to make the tray a client.
type peerConsultState struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Question   string `json:"question"` // truncated to maxStateQuestion — a menu row, not the message
	WaitingSec int    `json:"waiting_sec"`
}

// peerState is the peer-messaging slice of the snapshot: what needs a human, what came back, and what
// this machine answered on its own.
type peerState struct {
	Inbound  int `json:"inbound"`  // questions QUEUED for your approval on this machine
	Answered int `json:"answered"` // replies to your asks that landed and you haven't read
	// AutoAnswered is pure OBSERVABILITY: read-only answers this machine gave today with no human in
	// the loop (consult_budget.go's ledger doubles as the counter). It is the only trace of a flow
	// that is otherwise designed to be invisible, and a rise in it is what the tray announces.
	AutoAnswered int    `json:"auto_answered"`
	AutoProject  string `json:"auto_project,omitempty"` // label of the most recent one — a NAME, not content
	// Consults are the queued ones, oldest first, capped. Present only when something is queued.
	Consults []peerConsultState `json:"consults,omitempty"`
}

// maxStateQuestion / maxStateConsults bound what a poll can carry. A question is a menu row's worth or
// it is nothing; the row cap is what stops a burst of questions turning a 4s poll into a payload.
const (
	maxStateQuestion = 140
	maxStateConsults = 4
)

type machineState struct {
	Version string `json:"version"`
	// Env is the control plane this CLI is pointed at: "" for production (so the common case adds
	// nothing to the payload and reads as unadorned), else the short label — "staging",
	// "localhost:3111". API is the matching base URL, so a reader can open the RIGHT web app
	// instead of assuming partyline.sh.
	//
	// Both are omitempty: an older tray ignores them, and a newer tray against an older CLI sees
	// them absent, which is indistinguishable from production — the safe default, since production
	// is what an unlabelled tray has always meant.
	Env      string         `json:"env,omitempty"`
	API      string         `json:"api,omitempty"`
	Account  accountState   `json:"account"`
	Daemon   daemonState    `json:"daemon"`
	Sessions []sessionState `json:"sessions"`
	Waiting  int            `json:"waiting"` // sessions blocked on you — the number worth a badge
	// RateLimit is set when the model provider is currently refusing work on this machine. It's the
	// difference between "the fleet is quiet" and "the fleet is stopped", which look identical
	// otherwise — and the run that made this worth surfacing burned 2.7M tokens before anyone noticed.
	RateLimit *rateLimitState `json:"rate_limit,omitempty"`
	// Peers is omitted entirely when there is nothing to say, so an older tray (and any other reader)
	// sees exactly what it saw before.
	Peers *peerState `json:"peers,omitempty"`
}

func currentMachineState() machineState {
	acct := api.LoadAccount()
	ms := machineState{
		Version: version,
		Env:     api.EnvLabel(),
		API:     api.Base(),
		Account: accountState{LoggedIn: acct.Email != "", Email: acct.Email},
		Daemon:  currentDaemonState(),
	}
	if n := readRateLimitNote(); n != nil {
		rl := &rateLimitState{Note: n.Note, Run: n.Run}
		if !n.ResetAt.IsZero() {
			rl.ResetAt = n.ResetAt.Format(time.RFC3339)
		}
		ms.RateLimit = rl
	}
	ms.Peers = currentPeerState(time.Now())
	for _, s := range collectSessions() {
		if !s.Live {
			continue // the tray shows what's happening NOW; history lives in the web app
		}
		ms.Sessions = append(ms.Sessions, sessionState{
			ID:     s.ID,
			Tool:   s.Tool,
			Dir:    baseName(s.Cwd),
			Title:  clipTitle(s.Title),
			Status: s.Status,
		})
		if s.Status == "waiting" {
			ms.Waiting++
		}
	}
	return ms
}

// currentPeerState assembles the peer slice from LOCAL FILES ONLY. Returns nil when there is nothing
// to report, so the key is absent rather than a row of zeroes.
//
// `ptln state` IS A LOCAL READ. THE TRAY POLLS IT EVERY 4 SECONDS FROM A BACKGROUND PROCESS.
// Nothing on this path may make a network call, now or later. Wire an API fetch in here and you have
// built a 4s poll of the control plane on every user's machine, running whether or not anyone is
// looking at the menu — and one that fails the whole snapshot when the network hiccups. The three
// sources below are all on disk and all already pruned by their owners:
//
//	pending-consults.json  the daemon's queue of questions waiting on the owner (read-only view)
//	peer-messages.json     my asks and the answers that landed while I worked
//	consult-budget.json    today's auto-answer ledger, which doubles as the observability counter
//
// If a future version genuinely needs a remote read, it belongs behind a different command that the
// tray does not poll.
func currentPeerState(now time.Time) *peerState {
	ps := peerState{}
	for _, qc := range readPendingConsultsAt(pendingConsultsPath(), now) {
		ps.Inbound++
		if len(ps.Consults) >= maxStateConsults {
			continue // still counted; just not carried
		}
		ps.Consults = append(ps.Consults, peerConsultState{
			ID:         qc.Event.ConsultID,
			Project:    qc.Event.ProjectLabel,
			Question:   clipQuestion(qc.Event.Question),
			WaitingSec: int(now.Sub(qc.SeenAt) / time.Second),
		})
	}
	for _, m := range loadPeerMessagesAt(peerStorePath()) {
		// Answers to MY asks that I haven't read. Direction-checked so an inbound question can never
		// be counted as a reply I'm owed, and Delivered-checked because an answer already handed to
		// the asking agent (staged in its prompt, or returned in a tool result) is not one you have to
		// go and fetch from the menu — counting it would send you looking for something you have.
		if m.Direction == dirOutbound && m.Status == taskCompleted && !m.Read && !m.Delivered {
			ps.Answered++
		}
	}
	if b := readConsultBudget(); b.Total > 0 {
		ps.AutoAnswered, ps.AutoProject = b.Total, b.LastProject
	}
	if ps.Inbound == 0 && ps.Answered == 0 && ps.AutoAnswered == 0 {
		return nil
	}
	return &ps
}

// clipQuestion bounds a peer's question to a menu row. Newlines collapse (a menu item is one line) and
// the cut is by RUNE, so a multibyte question can't be sliced mid-character.
func clipQuestion(q string) string {
	r := []rune(strings.TrimSpace(strings.Join(strings.Fields(q), " ")))
	if len(r) <= maxStateQuestion {
		return string(r)
	}
	return string(r[:maxStateQuestion]) + "…"
}

// stateMain prints the snapshot as one JSON object. Always exits 0 with valid JSON when it can —
// the tray polls this, and a non-zero exit would read as "CLI missing" rather than "nothing running".
func stateMain() {
	b, err := json.Marshal(currentMachineState())
	if err != nil {
		fatal(fmt.Errorf("could not encode state: %w", err))
	}
	fmt.Fprintln(os.Stdout, string(b))
}

// baseName reduces a session cwd to its final component — "partyline" not "/Users/you/dev/partyline".
// The tray has one narrow menu, and the basename is what actually distinguishes two live sessions.
func baseName(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

// clipTitle bounds a session title for a menu bar row. Titles are the first user message verbatim,
// and a real one in testing ran to several hundred words — enough to blow the menu off the screen.
func clipTitle(t string) string {
	const max = 70
	r := []rune(strings.TrimSpace(strings.ReplaceAll(t, "\n", " ")))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}
