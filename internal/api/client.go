// Package api: the CLI's thin client for the partyline control plane.
// Mirrors docs/SYSTEM-DESIGN.md /api/v1 contract. Offline-first: everything
// here is optional sugar — the session engine never requires it.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func Base() string {
	v := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if v == "" {
		v = "https://partyline.sh" // production; PARTYLINE_API overrides for dev
	}
	return strings.TrimRight(v, "/")
}

func tokenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "token")
}

func SaveToken(tok string) error {
	if err := os.MkdirAll(filepath.Dir(tokenPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(tokenPath(), []byte(strings.TrimSpace(tok)+"\n"), 0o600)
}

func LoadToken() string {
	b, err := os.ReadFile(tokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ClearToken removes the saved token from this machine (logout). A no-op if
// there's nothing to remove. Does not revoke the token server-side — do that in
// the web app if a token may be compromised.
func ClearToken() error {
	if err := os.Remove(tokenPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func New() *Client {
	return &Client{Base: Base(), Token: LoadToken(), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// LatestVersion fetches the published CLI version info from the control plane.
// Unauthenticated and version-only (no token, no user data sent) — it's safe to
// call without login. Short timeout so a slow network never lingers.
func (c *Client) LatestVersion() (latest, minSupported, notice string, err error) {
	req, err := http.NewRequest("GET", c.Base+"/api/v1/version", nil)
	if err != nil {
		return "", "", "", err
	}
	hc := &http.Client{Timeout: 4 * time.Second}
	res, err := hc.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", "", "", fmt.Errorf("version: status %d", res.StatusCode)
	}
	var v struct {
		Latest       string `json:"latest"`
		MinSupported string `json:"min_supported"`
		Notice       string `json:"notice"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return "", "", "", err
	}
	return v.Latest, v.MinSupported, v.Notice, nil
}

func (c *Client) do(method, path string, in, out any) error {
	body := bytes.NewReader(nil)
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return fmt.Errorf("%s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// ----- device flow -----

type DeviceStart struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

func (c *Client) DeviceStart() (*DeviceStart, error) {
	var out DeviceStart
	if err := c.do("POST", "/api/v1/auth/device/start", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DevicePoll blocks until approved, expired, or deadline. It prints a live dot
// per poll so the user can see it's alive, and — crucially — does NOT spin
// silently for the full lifetime when the control plane is unreachable: after a
// continuous window of failures it bails with a clear error instead of hanging.
func (c *Client) DevicePoll(deviceCode string, interval, expiresIn int) (string, error) {
	if interval <= 0 {
		interval = 3
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	const maxOutage = 45 * time.Second // give up if we can't reach the CP this long
	var firstFail time.Time            // zero when the last poll succeeded
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)
		var out struct {
			Status      string `json:"status"`
			AccessToken string `json:"access_token"`
		}
		if err := c.do("POST", "/api/v1/auth/device/poll",
			map[string]string{"device_code": deviceCode}, &out); err != nil {
			if firstFail.IsZero() {
				firstFail = time.Now()
				fmt.Print("\n  ⚠ can't reach the control plane — retrying")
			} else {
				fmt.Print(".")
			}
			if time.Since(firstFail) >= maxOutage {
				return "", fmt.Errorf("lost contact with the control plane (%s) — check your connection and run `ptln login` again", c.Base)
			}
			continue
		}
		if !firstFail.IsZero() { // recovered from an outage
			fmt.Print(" reconnected\n  waiting for approval ")
			firstFail = time.Time{}
		}
		switch out.Status {
		case "ok":
			fmt.Println()
			return out.AccessToken, nil
		case "expired":
			return "", fmt.Errorf("code expired — run `ptln login` again")
		}
		fmt.Print(".") // pending — show progress
	}
	return "", fmt.Errorf("timed out waiting for approval — run `ptln login` again")
}

// ----- profile -----

type Profile struct {
	Handle         string `json:"handle"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	GithubUsername string `json:"github_username"`
	Timezone       string `json:"timezone"`
}

func (c *Client) Me() (*Profile, error) {
	var p Profile
	if err := c.do("GET", "/api/v1/me", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ----- teams (a "team" is a non-personal org; routes stay /api/v1/orgs) -----

type Org struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Personal bool   `json:"personal"`
	Role     string `json:"role"`
}
type Member struct {
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (c *Client) ListOrgs() ([]Org, error) {
	var out struct {
		Orgs []Org `json:"orgs"`
	}
	return out.Orgs, c.do("GET", "/api/v1/orgs", nil, &out)
}

// CreateOrg creates a Team (a non-personal org); the caller becomes owner.
func (c *Client) CreateOrg(name string) (*Org, error) {
	var o Org
	return &o, c.do("POST", "/api/v1/orgs", map[string]string{"name": name}, &o)
}

// CreatePartyOut is the result of creating a party from the CLI (`ptln party up`).
type CreatePartyOut struct {
	ID       string `json:"id"`
	JoinCode string `json:"join_code"`
	Org      string `json:"org"`
	Link     string `json:"link"` // runner join link; token in the #fragment
}

// CreateParty starts a party for a team (org slug) in the given mode and returns the
// join link. Same backend endpoint the web /parties/new and Slack /partyline party use.
func (c *Client) CreateParty(orgSlug, mode string) (*CreatePartyOut, error) {
	var o CreatePartyOut
	return &o, c.do("POST", "/api/v1/parties", map[string]string{"org_slug": orgSlug, "mode": mode}, &o)
}
func (c *Client) OrgMembers(slug string) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	return out.Members, c.do("GET", "/api/v1/orgs/"+slug+"/members", nil, &out)
}
func (c *Client) InviteOrg(slug, email, role string) error {
	return c.do("POST", "/api/v1/orgs/"+slug+"/invites", map[string]string{"email": email, "role": role}, nil)
}

// SetMemberAccess promotes/demotes a team member: 'full' = a billable driver seat
// (can be granted typing in a session), 'viewer' = watch-only.
func (c *Client) SetMemberAccess(slug, userID, access string) error {
	return c.do("PATCH", "/api/v1/orgs/"+slug+"/members/"+userID, map[string]string{"access": access}, nil)
}

// DefaultOrgSlug returns the personal org (or first) — used when --org is omitted.
func (c *Client) DefaultOrgSlug() (string, error) {
	orgs, err := c.ListOrgs()
	if err != nil {
		return "", err
	}
	for _, o := range orgs {
		if o.Personal {
			return o.Slug, nil
		}
	}
	if len(orgs) > 0 {
		return orgs[0].Slug, nil
	}
	return "", fmt.Errorf("no orgs")
}

// ----- sessions -----

type Endpoint struct {
	Type string `json:"type"`
	Addr string `json:"addr"`
	Port int    `json:"port,omitempty"`
}

type RegisterOut struct {
	ID       string `json:"id"`
	JoinCode string `json:"join_code"`
	Org      string `json:"org"`
	Relay    string `json:"relay"` // control-plane-assigned relay endpoint (host:port); "" = use client default
}

// RegisterSession creates a live session. key is the base64url Noise key (the
// #k= fragment); the control plane stores it so the web app and notifications
// can show the full join link to everyone authorized to see the session.
func (c *Client) RegisterSession(endpoints []Endpoint, visibility, orgSlug, key string, announce bool) (*RegisterOut, error) {
	in := map[string]any{"endpoints": endpoints, "visibility": visibility, "key": key}
	if orgSlug != "" {
		in["org_slug"] = orgSlug
	}
	if announce {
		in["announce"] = true
	}
	var out RegisterOut
	if err := c.do("POST", "/api/v1/sessions", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimSession turns a planned session (created from the web UI) live: the
// caller becomes the host, endpoints are attached, and pre-armed invites fire.
// key is stored as in RegisterSession (a planned session has no key until claim).
func (c *Client) ClaimSession(id string, endpoints []Endpoint, key string) (*RegisterOut, error) {
	var out RegisterOut
	if err := c.do("POST", "/api/v1/sessions/"+id+"/claim",
		map[string]any{"endpoints": endpoints, "key": key}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type SessionInfo struct {
	ID        string     `json:"id"`
	JoinCode  string     `json:"join_code"`
	Status    string     `json:"status"`
	StartedAt string     `json:"started_at"`
	Endpoints []Endpoint `json:"endpoints"`
	JoinLink  string     `json:"join_link"` // full link incl. key, for sessions you can join
	IsHost    bool       `json:"is_host"`
}

func (c *Client) ListSessions() ([]SessionInfo, error) {
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := c.do("GET", "/api/v1/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) ResolveCode(code string) (*SessionInfo, error) {
	var out SessionInfo
	if err := c.do("GET", "/api/v1/sessions/code/"+code, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) InviteSession(id string, emails []string) (int, error) {
	var out struct {
		Sent int `json:"sent"`
	}
	if err := c.do("POST", "/api/v1/sessions/"+id+"/invites",
		map[string]any{"targets": emails}, &out); err != nil {
		return 0, err
	}
	return out.Sent, nil
}

// SessionIdentity fetches a short-lived signed identity assertion for a code,
// which the joiner presents to the host over the E2EE channel. Requires login;
// returns "" with an error if the caller isn't authorized for the line.
func (c *Client) SessionIdentity(code string) (string, error) {
	var out struct {
		Assertion string `json:"assertion"`
	}
	if err := c.do("POST", "/api/v1/identity", map[string]any{"code": code}, &out); err != nil {
		return "", err
	}
	return out.Assertion, nil
}

// InviteTargets is the multi-target invite (emails, user ids, team ids, org).
func (c *Client) InviteTargets(id string, body map[string]any) (int, error) {
	var out struct {
		Sent  int `json:"sent"`
		Armed int `json:"armed"`
	}
	if err := c.do("POST", "/api/v1/sessions/"+id+"/invites", body, &out); err != nil {
		return 0, err
	}
	if out.Sent > 0 {
		return out.Sent, nil
	}
	return out.Armed, nil
}

// Heartbeat reports liveness/participants and returns whether the control plane
// considers this session ended (e.g. killed from the web) so the host can stop.
// A transient/network error returns ended=false — we never tear down a live
// local session on a flaky beat; only an explicit "ended" from the server does.
func (c *Client) Heartbeat(id string, participants []string) (ended bool) {
	var out struct {
		Ended bool `json:"ended"`
	}
	if err := c.do("POST", "/api/v1/sessions/"+id+"/heartbeat",
		map[string]any{"participants": participants}, &out); err != nil {
		return false
	}
	return out.Ended
}

func (c *Client) EndSession(id string, drivers int) {
	// drivers = distinct people who took the keyboard this session (#21). Web fires
	// the session_ended PostHog event from /end (keeps the analytics key server-side).
	_ = c.do("POST", "/api/v1/sessions/"+id+"/end", map[string]any{"drivers": drivers}, nil)
}

// ----- party (Mode 2: humans + agents coordination channel) -----

// ErrPartyClosed is returned by Stream when the channel rejects the token (the party
// ended or the token is invalid) — terminal, so the runner stops instead of looping.
var ErrPartyClosed = fmt.Errorf("party closed or token rejected")

// PartyMsg is one message on a party channel (matches the channel API's JSON).
type PartyMsg struct {
	ID        int64          `json:"id"`
	Sender    string         `json:"sender"` // "user:<h>" | "agent:<name>"
	Kind      string         `json:"kind"`
	Body      string         `json:"body"`
	Meta      map[string]any `json:"meta"` // {to:[names]} — resolved @addressing
	CreatedAt string         `json:"created_at"`
}

// PartyClient talks to the channel API with a PARTY-SCOPED token (not the login
// token). Base + ID + Token all come from the join link (.../p/<id>#t=<token>),
// so the runner needs no `ptln login`.
type PartyClient struct {
	Base  string
	ID    string
	Token string
}

// PartyInfo is the party's mode template — the system preamble to inject into the
// agent + the settings the runner reads (model, agent-turn brake). CLI flags override.
type PartyInfo struct {
	Mode         string `json:"mode"`
	SystemPrompt string `json:"system_prompt"`
	Settings     struct {
		Model         string `json:"model"`
		MaxAgentTurns *int   `json:"maxAgentTurns"` // pointer: distinguish 0 (off) from absent
	} `json:"settings"`
}

// Info fetches the party's mode + settings (party-token auth). Best-effort: the runner
// falls back to its defaults if this fails (older backend, transient error).
func (p *PartyClient) Info() (*PartyInfo, error) {
	req, err := http.NewRequest("GET", p.Base+"/api/v1/parties/"+p.ID+"/info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("info: %s", res.Status)
	}
	var out PartyInfo
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Post appends a message to the party as `name` (stored sender agent:<name>).
// Retries transient failures (network errors + 5xx, incl. the CPLN proxy's
// "upstream reset before headers") up to 3 attempts — a dropped reply is worse
// than a duplicate, and these posts are the agent's only voice. A 4xx is a real
// error (bad token, validation) and is returned immediately.
func (p *PartyClient) Post(name, body, kind string) (int64, error) {
	in := map[string]any{"name": name, "body": body}
	if kind != "" {
		in["kind"] = kind
	}
	b, _ := json.Marshal(in)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		req, err := http.NewRequest("POST", p.Base+"/api/v1/parties/"+p.ID+"/post", bytes.NewReader(b))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.Token)
		res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			lastErr = err // network/TLS — retry
			continue
		}
		if res.StatusCode >= 500 {
			res.Body.Close()
			lastErr = fmt.Errorf("server %s", res.Status) // transient — retry
			continue
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(res.Body).Decode(&e)
			if e.Error == "" {
				e.Error = res.Status
			}
			return 0, fmt.Errorf("%s", e.Error) // client error — do not retry
		}
		var out struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out.ID, nil
	}
	return 0, lastErr
}

// GetDoc fetches the party's shared working doc (EPIC A1). Best-effort — the runner
// injects it into the wake prompt, so an error just means "no doc this turn".
func (p *PartyClient) GetDoc() (body string, version int, err error) {
	req, err := http.NewRequest("GET", p.Base+"/api/v1/parties/"+p.ID+"/doc", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return "", 0, fmt.Errorf("doc %s", res.Status)
	}
	var out struct {
		Body    string `json:"body"`
		Version int    `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.Body, out.Version, nil
}

// ProposeEdit queues a pending section edit to the shared doc as agent:<name> (a human
// approves it before it merges). Section-scoped; new_body replaces the section's contents.
func (p *PartyClient) ProposeEdit(name, section, newBody string) (int64, error) {
	b, _ := json.Marshal(map[string]any{"name": name, "section": section, "new_body": newBody})
	req, err := http.NewRequest("POST", p.Base+"/api/v1/parties/"+p.ID+"/doc/propose", bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return 0, fmt.Errorf("%s", e.Error)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out.ID, nil
}

// Stream opens the SSE channel feed and invokes onMsg per message until ctx is
// cancelled or the connection drops (returns nil/err so the caller can reconnect
// with ?since). onReady fires once the initial backlog has been flushed (the
// `: ready` comment), letting the runner distinguish history from live traffic.
// A no-timeout HTTP client is used — the request stays open for the stream's life.
func (p *PartyClient) Stream(
	ctx context.Context,
	name, role string,
	since int64,
	onReady func(),
	onMsg func(PartyMsg),
) error {
	u := fmt.Sprintf("%s/api/v1/parties/%s/stream?name=%s&role=%s&since=%d",
		p.Base, p.ID, url.QueryEscape(name), url.QueryEscape(role), since)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// 401/403 = the party token no longer resolves an OPEN party (party ended, or a
	// bad token). That's terminal — the caller should stop, not reconnect forever.
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrPartyClosed
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("stream: %s", res.Status)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // allow long message lines
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data: "):
			var m PartyMsg
			if json.Unmarshal([]byte(line[6:]), &m) == nil && m.ID > 0 {
				onMsg(m)
			}
		case line == ": ready":
			if onReady != nil {
				onReady()
			}
		}
	}
	return sc.Err()
}

// Recent returns the channel backlog — the recent messages the stream replays on connect,
// before its ": ready" marker. There's no one-shot messages endpoint, so we open the
// stream, collect what it replays, and disconnect at ": ready". Best-effort: returns
// whatever arrived; only a closed/invalid party (ErrPartyClosed) surfaces as an error.
func (p *PartyClient) Recent(ctx context.Context, name string) ([]PartyMsg, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var msgs []PartyMsg
	err := p.Stream(ctx, name, "", 0,
		func() { cancel() }, // backlog fully replayed → stop reading and disconnect
		func(m PartyMsg) { msgs = append(msgs, m) },
	)
	if err == ErrPartyClosed {
		return nil, err
	}
	return msgs, nil
}
