package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Instance setup — the CLI/agent half of ONE endpoint with three surfaces.
//
// /api/v1/setup is the single answer to "is this instance configured, and what does it still need".
// The first-run wizard, the settings page and the MCP setup tools all call it, deliberately: three
// implementations of "configure partyline" is three answers to "is it set up", and the first time
// they disagree is on somebody else's box where nobody can look. Nothing here re-derives a step or
// re-decides completion — this is a typed transport for what the endpoint already computed.
//
// REFERENCE, NOT COMMAND. Everything below sends DATA to two fixed paths. There is no caller-
// supplied path segment, no query string, and no generic "call any endpoint" helper: the only
// inputs are two typed fields on a JSON body, so a model steering these can never point them at
// another endpoint or turn a value into an argv.

// SetupSettings is what the APP is allowed to change about itself. Everything else on a self-hosted
// box (the issuer, the database, the domain) is environment written before the container starts, and
// is reported by the steps rather than settable here.
type SetupSettings struct {
	InstanceName string `json:"instance_name"`
	AllowSignups bool   `json:"allow_signups"`
	// SetupCompletedAt records that someone has been through setup once. It is NOT what decides
	// whether the instance is set up — see SetupState.Complete, which is recomputed from the world.
	SetupCompletedAt string `json:"setup_completed_at"`
}

// SetupStep mirrors web/src/lib/api/setup-steps.ts one field at a time, including its camelCase JSON
// keys. That file is the only place a step is decided; duplicating the derivation here is exactly
// the disagreement this endpoint exists to prevent.
type SetupStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// State is "done" | "todo" | "blocked". Blocked means an earlier step is holding it up, which is
	// a different fact from "not done yet" — you cannot start it.
	State string `json:"state"`
	// Detail is what to do about it, in the second person. Empty when done.
	Detail string `json:"detail"`
	// BlockedBy names the step holding this one up, when blocked.
	BlockedBy string `json:"blockedBy"`
	// EnvOnly marks the steps no API call can finish: a person must edit .env and restart.
	EnvOnly bool `json:"envOnly"`
}

// SetupState is the whole GET response: the settings, every step already answered, the one step to
// do next (nil when there is nothing left), and whether the instance is set up.
type SetupState struct {
	Settings SetupSettings `json:"settings"`
	Steps    []SetupStep   `json:"steps"`
	Next     *SetupStep    `json:"next"`
	Complete bool          `json:"complete"`
}

// The setup errors a caller must be able to tell apart, because each has a different fix.
var (
	// ErrSetupNotSignedIn is a 401. On a fresh instance the GET is readable without signing in
	// (there is nothing to disclose when no account exists yet), so a 401 here means accounts DO
	// exist and this machine has no token — `ptln login` is the fix.
	ErrSetupNotSignedIn = errors.New("not signed in to this instance")
	// ErrSetupForbidden is a 403: signed in, but not the instance administrator. Surfaced as its own
	// error so the caller can say who CAN do it instead of reporting a bare failure.
	ErrSetupForbidden = errors.New("only an administrator of this instance can change its settings")
	// ErrSetupNothingToChange is raised locally, before any request, when a patch carries no fields.
	ErrSetupNothingToChange = errors.New("nothing to change")
)

// Setup reads this instance's setup state (GET /api/v1/setup).
//
// Unauthenticated-readable on a virgin instance by design, so it is called with whatever token this
// machine has — including none.
func (c *Client) Setup() (*SetupState, error) {
	var out SetupState
	if err := c.setupDo(http.MethodGet, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSetup changes what the app may change (PATCH /api/v1/setup), instance-admin only.
//
// Pointers, not a map: an absent field must be distinguishable from a zero one (allow_signups=false
// is a real instruction, not a missing argument), and a typed pair is also the reason no caller can
// smuggle an extra key into the body. Returns the settings as they now stand.
func (c *Client) UpdateSetup(instanceName *string, allowSignups *bool) (*SetupSettings, error) {
	body := map[string]any{}
	if instanceName != nil {
		body["instance_name"] = *instanceName
	}
	if allowSignups != nil {
		body["allow_signups"] = *allowSignups
	}
	if len(body) == 0 {
		return nil, ErrSetupNothingToChange
	}
	var out struct {
		Settings SetupSettings `json:"settings"`
	}
	// Single-shot, NOT retried — the retry rule in this package (see PartyClient.doWithRetry and its
	// callers): idempotent reads may be retried, state changes may not, because a retry after an
	// ambiguous timeout applies the change twice.
	if err := c.setupDo(http.MethodPatch, body, &out); err != nil {
		return nil, err
	}
	return &out.Settings, nil
}

// setupDo is the transport for both calls above. The path is a constant; the only variable is the
// JSON body. Errors never echo the request (no URL, no headers, no token) — a wrapped *url.Error
// would carry the request URL, so transport failures become a fixed message, exactly as getRun does.
func (c *Client) setupDo(method string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return errors.New("could not encode the request")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+"/api/v1/setup", body)
	if err != nil {
		return errors.New("could not build the request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("could not reach this partyline instance")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == 401:
		return ErrSetupNotSignedIn
	case res.StatusCode == 403:
		return ErrSetupForbidden
	case res.StatusCode == 400:
		// The endpoint's 400s are its VALIDATION messages ("instance_name must be 1–60 characters"),
		// which are fixed server-authored strings and are the useful half of the failure. Bounded and
		// stripped of newlines so a message can never be long enough, or shaped enough, to reframe
		// the tool output it lands in.
		return errors.New(setupErrorText(res))
	case res.StatusCode >= 400:
		// Status only — never the body, which on an unexpected error could echo input back.
		return fmt.Errorf("this partyline instance returned %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return errors.New("could not decode this instance's response")
	}
	return nil
}

// setupErrorText pulls the `error` field out of a 400 and makes it safe to quote.
func setupErrorText(res *http.Response) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&e)
	msg := strings.Join(strings.Fields(e.Error), " ")
	if msg == "" {
		return "this instance rejected the change (400)"
	}
	// Runes, not bytes: the endpoint's own messages contain an en dash, and a byte slice through one
	// produces mojibake in the exact place a caller is trying to read an explanation.
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return msg
}
