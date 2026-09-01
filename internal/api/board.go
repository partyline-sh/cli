package api

import (
	"fmt"
	"net/url"
	"strings"
)

// The work board, as a Go value — the read that backs both the agent-facing read_board tool and
// `ptln board`, the terminal board.
//
// These types used to live in client.go carrying only the eight fields read_board printed. The
// board TUI needs what the web card renders (why a card is stuck, whether its machine went quiet,
// what the agent last said), and all of it was already in the payload — the Go side simply never
// asked for it. Widening the struct costs nothing on the wire: the endpoint hands back the same
// JSON either way.

// BoardColumn is a column key. These are the API's own keys, not display labels — the last column's
// key is "accepted" while the web labels it "Accepted" (see work-view.ts for why the key is
// load-bearing and cannot be renamed without a migration).
type BoardColumn string

const (
	ColBacklog  BoardColumn = "backlog"
	ColBuilding BoardColumn = "building"
	ColBlocked  BoardColumn = "blocked"
	ColReview   BoardColumn = "review"
	ColAccepted BoardColumn = "accepted"
)

// BoardColumns is the board's left-to-right order — the one place that order is written down.
var BoardColumns = []BoardColumn{ColBacklog, ColBuilding, ColBlocked, ColReview, ColAccepted}

// Title is the human label for a column. "Accepted" rather than "Shipped" deliberately: partyline
// cannot know what a customer's pipeline does after a merge, and claiming otherwise left merged
// PRs sitting unpromoted behind a card that said it had shipped.
func (c BoardColumn) Title() string {
	switch c {
	case ColBacklog:
		return "Backlog"
	case ColBuilding:
		return "Building"
	case ColBlocked:
		return "Blocked"
	case ColReview:
		return "Review"
	case ColAccepted:
		return "Accepted"
	}
	return string(c)
}

// BoardCard is one card as the work board renders it.
type BoardCard struct {
	ID    string `json:"id"`
	Title string `json:"title"` // the project label the run targets
	Task  string `json:"task"`  // first task in the worklist, as a preview

	Column BoardColumn `json:"column"`
	Status string      `json:"status"` // raw run status — drives which actions are legal

	// Unscheduled work: a work_item in `planning`, on the board with no run behind it yet. Every
	// run-shaped field below is empty for such a card, and starting it PROMOTES the item (creating
	// the run) rather than starting a run that already exists.
	Unscheduled bool `json:"unscheduled"`
	Archived    bool `json:"archived"`

	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Machine string `json:"machine"`
	Preset  string `json:"preset"`
	Engine  string `json:"engine"`

	CreatedAt     string `json:"createdAt"`
	NeedsApproval bool   `json:"needsApproval"`
	Failed        bool   `json:"failed"`
	Started       bool   `json:"started"` // has a Building run streamed its first step yet?
	Detail        string `json:"detail"`  // the run's status message — for a failed card, the reason
	PRURL         string `json:"prUrl"`

	// Why a DONE run shipped nothing, when it shipped nothing. Three kinds need three different
	// human actions, which is why this is not a boolean.
	NoPR *struct {
		Kind   string `json:"kind"` // branch-only | pr-failed | no-changes
		Detail string `json:"detail"`
	} `json:"noPr"`

	Rank    float64 `json:"rank"`
	ChainID string  `json:"chainId"`
	Owner   string  `json:"owner"`

	// The four ways a Building card can be not-moving. They look identical in a runs table and mean
	// completely different things to the person reading the board, so each is its own flag.
	Stalled            bool `json:"stalled"`            // daemon went stale (>3min) — stuck
	MachineLocked      bool `json:"machineLocked"`      // pinned daemon below MIN_SUPPORTED — dispatch is version-locked
	ChainWaiting       bool `json:"chainWaiting"`       // blocked behind an earlier chain member — idle BY DESIGN
	ConcurrencyWaiting bool `json:"concurrencyWaiting"` // held back by the team's concurrency cap — waiting for a slot

	// Set when the member holding the chain up is parked on a HUMAN: the waiting card then says
	// whose move it is and names the run that needs the decision.
	ChainBlocker *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Task   string `json:"task"`
	} `json:"chainBlocker"`

	LastLine   string `json:"lastLine"` // most recent streamed step — the card's live "what it's doing"
	LastLineAt string `json:"lastLineAt"`

	ParentLabel string `json:"parentLabel"` // work-item parent (epic/feature title) — the swimlane badge
	ReviewGrade string `json:"reviewGrade"` // the review agent's latest grade (A–F)
	Attention   bool   `json:"attention"`   // finished WITH FINDINGS: not done, not failed, needs eyes

	ReviewRunID   string `json:"reviewRunId"`
	Reviewing     bool   `json:"reviewing"`
	ReviewWaiting bool   `json:"reviewWaiting"`

	Conflict *struct {
		Count      int  `json:"count"`
		Resolvable bool `json:"resolvable"`
	} `json:"conflict"`

	ItemID       string `json:"itemId"`
	ItemThreadID string `json:"itemThreadId"`
	// Readiness is a *int because 0 and "not from a plan" are different answers, and the start gate
	// only applies to the first.
	Readiness     *int   `json:"readiness"`
	ReadinessNote string `json:"readinessNote"`

	// ── cards that came from somewhere else ──────────────────────────────────────────────────────
	//
	// A board provider (Odoo, Jira, Linear — see docs/plans/board-providers.md) fills a SUBSET of
	// the fields above and these three. They live on the same struct rather than in a parallel type
	// so the renderer, the cursor model and the detail pane stay one code path: a foreign card is a
	// card with fewer fields, not a different kind of thing.

	// Foreign marks a card partyline does not own. It gates everything that would act on a run:
	// boardActions offers nothing (there is no run to accept or restart), and the only move is to
	// import it as partyline work.
	Foreign bool `json:"-"`
	// SourceURL is the item in ITS OWN tracker — what `o` opens for a foreign card, where a
	// partyline card opens its PR.
	SourceURL string `json:"-"`
	// StateLabel is the state word a provider wants shown, since a foreign card has no run status
	// for cardState to reason about.
	StateLabel string `json:"-"`
}

// MinStartReadiness mirrors MIN_START_READINESS in work-view.ts: below this floor a plan item still
// owes the human an answer, so starting it would dispatch a half-specified task. A card with no
// plan item (Readiness nil) is never gated.
const MinStartReadiness = 4

// StartBlockedByReadiness reports whether starting this card is gated on an unanswered question.
func (c BoardCard) StartBlockedByReadiness() bool {
	return c.Readiness != nil && *c.Readiness < MinStartReadiness
}

// Board is the five columns. The last field's JSON key is "accepted", which is what the endpoint
// actually emits — it was `json:"shipped"` until the terminal board went looking for the column and
// found it permanently empty. Every Go reader of the board (read_board included) had been showing
// an empty Accepted column since the key changed on the web side.
type Board struct {
	Backlog  []BoardCard `json:"backlog"`
	Building []BoardCard `json:"building"`
	Blocked  []BoardCard `json:"blocked"`
	Review   []BoardCard `json:"review"`
	Accepted []BoardCard `json:"accepted"`
}

// Column returns one column by key, so callers can iterate BoardColumns instead of writing the
// five-way switch again.
func (b *Board) Column(c BoardColumn) []BoardCard {
	switch c {
	case ColBacklog:
		return b.Backlog
	case ColBuilding:
		return b.Building
	case ColBlocked:
		return b.Blocked
	case ColReview:
		return b.Review
	case ColAccepted:
		return b.Accepted
	}
	return nil
}

// Find locates a card by id anywhere on the board.
func (b *Board) Find(id string) (BoardCard, bool) {
	for _, col := range BoardColumns {
		for _, c := range b.Column(col) {
			if c.ID == id {
				return c, true
			}
		}
	}
	return BoardCard{}, false
}

// ReadBoard returns the work board exactly as the web renders it. There was previously no way to ask
// this question at all outside a browser, so an agent could file and start work but never see it.
func (c *Client) ReadBoard() (*Board, error) {
	var r struct {
		Board Board `json:"board"`
	}
	if err := c.do("GET", "/api/v1/board", nil, &r); err != nil {
		return nil, err
	}
	return &r.Board, nil
}

// RunAction fires one of the run lifecycle endpoints (start, accept, requeue, retry, restart,
// resume, discard, archive, cancel, pause, unpause, rebase, open-pr, withdraw-pr).
//
// One method rather than fifteen because the server, not the client, decides whether a transition
// is legal: each endpoint status-guards its own run and answers 409 when the card already moved.
// A client-side copy of that matrix would be a second opinion that drifts. What the CLI DOES own is
// which actions to OFFER (see boardActions) — offering is a UI question, permitting is the
// server's.
//
// force=true adds ?force=1, which the resume path reads as "a human clicked this": a run paused for
// a predicted quota reset refuses an unattended resume until the time passes, but a person asking
// again is new information (credits added, plan changed) and always overrides the wait.
func (c *Client) RunAction(runID, action string, force bool) (map[string]any, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("no run id")
	}
	path := "/api/v1/runs/" + url.PathEscape(runID) + "/" + action
	if force {
		path += "?force=1"
	}
	var out map[string]any
	if err := c.do("POST", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RunLogs reads a run's streamed step output, oldest first — the tail the board's detail pane
// shows so you can watch a build without opening the run in a browser.
//
// It reads back into RunLogLine, the same type crank WRITES with (AppendRunLogs). The read endpoint
// omits task_idx, so that field lands nil here; seq/stream/body are the same three fields on both
// sides. One type for one row, rather than a second near-identical struct that would drift.
func (c *Client) RunLogs(runID string) ([]RunLogLine, error) {
	var r struct {
		Logs []RunLogLine `json:"logs"`
	}
	if err := c.do("GET", "/api/v1/runs/"+url.PathEscape(runID)+"/logs", nil, &r); err != nil {
		return nil, err
	}
	return r.Logs, nil
}

// SetRunRank moves a backlog card in the queue. Rank is strictly descending within a chain, so this
// is also how a chain's order changes.
func (c *Client) SetRunRank(runID string, rank float64) error {
	return c.do("POST", "/api/v1/runs/"+url.PathEscape(runID)+"/rank", map[string]any{"rank": rank}, nil)
}

// DeleteWorkItem removes a PLANNED item — one that has no run behind it. The endpoint refuses with
// 409 when the item still has children, which is the safe answer: deleting a parent out from under
// a subtree would orphan work somebody planned.
func (c *Client) DeleteWorkItem(itemID string) error {
	if strings.TrimSpace(itemID) == "" {
		return fmt.Errorf("no item id")
	}
	return c.do("DELETE", "/api/v1/work-items/"+url.PathEscape(itemID), nil, nil)
}

// LabelAssignment is one machine's progress binding a project, as the assignments wire reports it:
// queued → cloning → registering → ready, or failed with the daemon's own reason.
type LabelAssignment struct {
	DaemonID string `json:"daemon_id"`
	Machine  string `json:"machine"`
	Online   bool   `json:"online"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
}

// Assignments reports how the fleet is getting on with one project label.
//
// The purpose-built read for "did that bind land?", rather than inferring it from what the machines
// advertise: a clone takes minutes and passes through states worth showing, and `reason` carries
// the daemon's own failure text so a stuck machine explains itself.
func (c *Client) Assignments(label string) ([]LabelAssignment, error) {
	var r struct {
		Assignments []LabelAssignment `json:"assignments"`
	}
	if err := c.do("GET", "/api/v1/daemon/assignments?label="+url.QueryEscape(label), nil, &r); err != nil {
		return nil, err
	}
	return r.Assignments, nil
}
