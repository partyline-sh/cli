package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Usage is the per-run token accounting claude reports via `-p --output-format
// json` (and on stream-json result events). Other engines report nothing yet —
// a zero Usage means "no usage seen", which callers treat as unknown, never fatal.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Total is a crude "tokens this run touched" sum — the signal the O.5 ceiling
// accumulates. It is a safety net, not a billing ledger: cache reads are counted
// the same as fresh input on purpose (over-count, never under-count, so an
// unattended run stops sooner rather than later). NOT for display — see Fresh.
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// Fresh is the genuinely-new I/O — fresh (non-cached) input + generated output —
// and the honest number to SHOW a human. It matches the intuition behind the
// token count Claude Code prints in its own footer (tens of thousands for a
// session, not millions). It EXCLUDES both cache fields on purpose:
//   - cache_read: re-reads of an already-paid cached prefix, re-charged every
//     turn — this is what balloons Total into the millions for no new work.
//   - cache_creation: the one-time WRITE of context (file reads, system prompt)
//     into the cache. It's real cost but it's bulk context overhead, not "work
//     the run did", and including it overstated Fresh by ~10× (caching routes
//     most new content through cache_creation, so input stays small).
//
// The dollar cost (total_cost_usd) is the figure that DOES account for both cache
// fields at their real rates, shown alongside; CacheReadInputTokens is exposed
// as-is for a muted "+N cached" detail.
func (u Usage) Fresh() int {
	return u.InputTokens + u.OutputTokens
}

// Result is a one-shot run's parsed outcome: the final answer text, the engine's
// opaque resume handle ("" = restart-only), and — when the engine reports them —
// token usage and cost. Only claude fills SessionID/Usage/CostUSD today.
type Result struct {
	Text      string
	SessionID string
	Usage     Usage
	CostUSD   float64
}

// claudeEnvelope is the subset of claude's `-p --output-format json` object we
// read: the final answer text, token usage, the session id (the opaque resume
// handle), and the run's cost.
type claudeEnvelope struct {
	Result    string  `json:"result"`
	Usage     Usage   `json:"usage"`
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"total_cost_usd"`
}

// ParseResult reads a one-shot run's stdout into a Result. For claude that is
// the `-p --output-format json` envelope — a malformed envelope is an error, so
// callers can fall back to the raw output explicitly. Every other engine has no
// structured result format: the whole stdout is the Text, with no session id or
// usage.
func (s Spec) ParseResult(stdout []byte) (Result, error) {
	if s.Name != "claude" {
		return Result{Text: string(stdout)}, nil
	}
	var env claudeEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		return Result{}, fmt.Errorf("claude result envelope: %w", err)
	}
	return Result{Text: env.Result, SessionID: env.SessionID, Usage: env.Usage, CostUSD: env.CostUSD}, nil
}
