package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

// Run READ-ONLY diagnosis reads (user-authed, GET only). These back the MCP `read_run` /
// `read_run_log` tools: an agent can inspect one run's state and step output without a logged-in
// browser. Everything here is a GET — there is deliberately NO generic "fetch any path" helper, so
// a caller (or a model steering one) can never point these at another endpoint: the only input is a
// run id, and the path is built HERE from that validated id.
//
// Visibility is decided server-side by RLS through the caller's account token — exactly the same
// policies the web UI reads under (runs: team members + creator). The client adds no trust of its own.

// readRunIDRe validates a run id BEFORE it is interpolated into a request path. RLS is the real
// authorization boundary, but a URL must never be built from unvalidated caller-supplied input —
// so anything that isn't a canonical UUID is rejected here, without a request being made.
var readRunIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrRunNotVisible is the ONE error for 401/403/404. Collapsing them is deliberate: distinguishing
// "exists but you may not see it" from "does not exist" would leak the existence of another org's
// run id to anyone who can guess one.
var ErrRunNotVisible = errors.New("no run with that id is visible to your account")

// ErrBadRunID is returned for a non-UUID run id (no request is made).
var ErrBadRunID = errors.New("run id must be a UUID")

// RunRow is the run's stored state — the header a diagnosis starts from.
type RunRow struct {
	ID           string   `json:"id"`
	ProjectLabel string   `json:"project_label"`
	ThreadID     string   `json:"thread_id"`
	Preset       string   `json:"preset"`
	Status       string   `json:"status"`
	Tasks        []string `json:"tasks"` // the worklist (DATA the worker resolves), not argv
	MergePolicy  string   `json:"merge_policy"`
	MaxTokens    *int64   `json:"max_tokens"`
	Detail       string   `json:"detail"`
	Engine       string   `json:"engine"`
	Model        string   `json:"model"`
	DaemonID     string   `json:"daemon_id"`
	ChainID      string   `json:"chain_id"`
	CreatedAt    string   `json:"created_at"`
	DecidedAt    string   `json:"decided_at"`
	AcceptedAt   string   `json:"accepted_at"`
	ResumeAt     string   `json:"resume_at"`
}

// RunTaskRow is one worklist entry's lifecycle row (the run_tasks projection). Empty for a run whose
// worker never claimed anything — which is itself the answer to "did the worker ever start?".
type RunTaskRow struct {
	Idx             int    `json:"idx"`
	Task            string `json:"task"`
	Status          string `json:"status"`
	Branch          string `json:"branch"`
	PRURL           string `json:"pr_url"`
	Detail          string `json:"detail"`
	Summary         string `json:"summary"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at"`
	DurationMS      *int64 `json:"duration_ms"`
	FreshTokens     *int64 `json:"fresh_tokens"`
	CacheReadTokens *int64 `json:"cache_read_tokens"`
	ClaimedByLabel  string `json:"claimed_by_label"`
	// #602: the branch's verified state as the daemon probed it at completion — the answer to "did
	// this produce anything, is it still there, has it landed?". Nil = never probed, which is a
	// different fact from a probe that ran and could not tell.
	BranchState *BranchState `json:"branch_state"`
}

// RunPlanItem is the plan item this run was promoted from — its readiness is the "was this
// well-specified before it ran?" half of a diagnosis.
type RunPlanItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Readiness     *int   `json:"readiness"`
	ReadinessNote string `json:"readiness_note"`
}

// RunChainLink is one member of the run's chain, in execution order — so "never started" can be
// explained by an earlier member still holding the chain.
type RunChainLink struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Task   string `json:"task"`
}

// RunSnapshot is the whole read-only view of one run.
type RunSnapshot struct {
	Run      RunRow         `json:"run"`
	Tasks    []RunTaskRow   `json:"tasks"`
	PlanItem *RunPlanItem   `json:"plan_item"`
	Chain    []RunChainLink `json:"chain"`
}

// GetRun reads one run's state (GET /api/v1/runs/{id}) with the user's account token. Returns
// ErrRunNotVisible for 401/403/404 (see above) and ErrBadRunID for a non-UUID id.
func (c *Client) GetRun(runID string) (*RunSnapshot, error) {
	if !readRunIDRe.MatchString(runID) {
		return nil, ErrBadRunID
	}
	var out RunSnapshot
	if err := c.getRun("/api/v1/runs/"+runID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRunLogs reads the run's step-output lines (GET /api/v1/runs/{id}/logs), oldest first. The
// endpoint returns the whole stream; the CALLER must bound what it shows a model — logs are
// unbounded in principle (see the MCP tool, which tails + byte-caps + redacts). No query string:
// the path is the id and nothing else.
func (c *Client) GetRunLogs(runID string) ([]RunLogLine, error) {
	if !readRunIDRe.MatchString(runID) {
		return nil, ErrBadRunID
	}
	var out struct {
		Logs []RunLogLine `json:"logs"`
	}
	if err := c.getRun("/api/v1/runs/"+runID+"/logs", &out); err != nil {
		return nil, err
	}
	return out.Logs, nil
}

// getRun is the read-only transport for the two calls above: a GET, the token ONLY in the
// Authorization header, and errors that never echo the request (no URL, no headers, no token) —
// a wrapped *url.Error would carry the request URL, so transport failures are replaced with a
// fixed message. 401/403/404 all collapse to ErrRunNotVisible.
func (c *Client) getRun(path string, out any) error {
	req, err := http.NewRequest("GET", c.Base+path, nil)
	if err != nil {
		return errors.New("could not build the request")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("could not reach the partyline control plane")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404:
		return ErrRunNotVisible
	case res.StatusCode >= 400:
		// Status text only — never the body, which could echo back input we sent.
		return fmt.Errorf("the control plane returned %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return errors.New("could not decode the control plane's response")
	}
	return nil
}
