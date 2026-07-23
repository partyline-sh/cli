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

type machineState struct {
	Version  string         `json:"version"`
	Account  accountState   `json:"account"`
	Daemon   daemonState    `json:"daemon"`
	Sessions []sessionState `json:"sessions"`
	Waiting  int            `json:"waiting"` // sessions blocked on you — the number worth a badge
	// RateLimit is set when the model provider is currently refusing work on this machine. It's the
	// difference between "the fleet is quiet" and "the fleet is stopped", which look identical
	// otherwise — and the run that made this worth surfacing burned 2.7M tokens before anyone noticed.
	RateLimit *rateLimitState `json:"rate_limit,omitempty"`
}

func currentMachineState() machineState {
	acct := api.LoadAccount()
	ms := machineState{
		Version: version,
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
