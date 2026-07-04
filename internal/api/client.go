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

// Account is the locally-cached identity behind the saved token: enough to show
// "logged in as …" in the UI without a network round-trip on every launch. It's
// a convenience mirror of the profile, refreshed at login — never the source of
// truth (the token is). Daemons get registered under whichever account is logged
// in here, so surfacing it stops the cross-account "where are my bots?" confusion.
type Account struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

func accountPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "account.json")
}

// SaveAccount caches the identity next to the token (best-effort; a write failure
// just means the UI falls back to a lazy fetch).
func SaveAccount(a Account) error {
	if err := os.MkdirAll(filepath.Dir(accountPath()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return os.WriteFile(accountPath(), b, 0o600)
}

// LoadAccount returns the cached identity, or a zero Account if none is cached.
func LoadAccount() Account {
	var a Account
	b, err := os.ReadFile(accountPath())
	if err != nil {
		return a
	}
	_ = json.Unmarshal(b, &a)
	return a
}

// ClearAccount drops the cached identity (logout). A no-op if absent.
func ClearAccount() error {
	if err := os.Remove(accountPath()); err != nil && !os.IsNotExist(err) {
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

// ----- Common Ground: threads + context blocks -----

type Thread struct {
	ID         string `json:"id"`
	OrgID      string `json:"org_id"`
	Title      string `json:"title"`
	Visibility string `json:"visibility"` // "private" | "team"
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
}

type ContextBlock struct {
	ID        int64    `json:"id"`
	Kind      string   `json:"kind"`
	Body      string   `json:"body"`
	Author    string   `json:"author"`
	Engine    string   `json:"engine"`
	Status    string   `json:"status"`
	Entities  []string `json:"entities,omitempty"` // slugs of what the fact is about (E7.1)
	CreatedAt string   `json:"created_at"`
}

// ListThreads returns the threads the logged-in user can see (own + shared-to-their-team).
func (c *Client) ListThreads() ([]Thread, error) {
	var r struct {
		Threads []Thread `json:"threads"`
	}
	if err := c.do("GET", "/api/v1/threads", nil, &r); err != nil {
		return nil, err
	}
	return r.Threads, nil
}

// CreateThread makes a thread. orgSlug "" → the user's personal org; visibility "" → private.
func (c *Client) CreateThread(title, orgSlug, visibility string) (*Thread, error) {
	in := map[string]string{"title": title}
	if orgSlug != "" {
		in["org_slug"] = orgSlug
	}
	if visibility != "" {
		in["visibility"] = visibility
	}
	var r struct {
		Thread Thread `json:"thread"`
	}
	if err := c.do("POST", "/api/v1/threads", in, &r); err != nil {
		return nil, err
	}
	return &r.Thread, nil
}

// GetThread returns one thread + its current block feed (RLS-gated).
func (c *Client) GetThread(id string) (*Thread, []ContextBlock, error) {
	var r struct {
		Thread Thread         `json:"thread"`
		Blocks []ContextBlock `json:"blocks"`
	}
	if err := c.do("GET", "/api/v1/threads/"+id, nil, &r); err != nil {
		return nil, nil, err
	}
	return &r.Thread, r.Blocks, nil
}

// SetThreadVisibility shares ("team") or unshares ("private") a thread (owner only). "team"
// without a specific team shares within the thread's current org.
func (c *Client) SetThreadVisibility(id, visibility string) error {
	return c.do("PATCH", "/api/v1/threads/"+id, map[string]string{"visibility": visibility}, nil)
}

// ShareThreadWithTeam moves a thread to a specific team (by slug) and marks it team-visible —
// how a personal thread becomes shared with a real team. Owner + team member only (server-checked).
func (c *Client) ShareThreadWithTeam(id, orgSlug string) error {
	return c.do("PATCH", "/api/v1/threads/"+id, map[string]string{"visibility": "team", "org_slug": orgSlug}, nil)
}

// SetThreadArchived archives/unarchives a thread (owner only).
func (c *Client) SetThreadArchived(id string, archived bool) error {
	return c.do("PATCH", "/api/v1/threads/"+id, map[string]bool{"archived": archived}, nil)
}

// MarkConnected records (once, on a connected session's start) that this user + engine has a
// session on the thread — MVP presence for the web's worktree/agent board. machine/branch say
// WHERE it runs (hostname + git branch, "" when unknown). Best-effort; callers ignore the error.
func (c *Client) MarkConnected(threadID, engine, machine, branch string) error {
	return c.do("POST", "/api/v1/threads/"+threadID+"/presence",
		map[string]string{"engine": engine, "machine": machine, "branch": branch}, nil)
}

// RecallEntity is Recall scoped to one entity slug ("" = the whole feed) — E7.1.
func (c *Client) RecallEntity(threadID, entity string) ([]ContextBlock, error) {
	path := "/api/v1/threads/" + threadID + "/blocks"
	if entity != "" {
		path += "?entity=" + url.QueryEscape(entity)
	}
	var r struct {
		Blocks []ContextBlock `json:"blocks"`
	}
	if err := c.do("GET", path, nil, &r); err != nil {
		return nil, err
	}
	return r.Blocks, nil
}

// Recall returns a thread's blocks after sinceID (0 = all).
func (c *Client) Recall(threadID string, sinceID int64) ([]ContextBlock, error) {
	path := "/api/v1/threads/" + threadID + "/blocks"
	if sinceID > 0 {
		path += fmt.Sprintf("?since=%d", sinceID)
	}
	var r struct {
		Blocks []ContextBlock `json:"blocks"`
	}
	if err := c.do("GET", path, nil, &r); err != nil {
		return nil, err
	}
	return r.Blocks, nil
}

// Remember appends a context block. agent "" → authored as the user; engine/supersedes optional.
func (c *Client) Remember(threadID, kind, body, agent, engine string, supersedes int64, entities []string) (*ContextBlock, error) {
	in := map[string]any{"kind": kind, "body": body}
	if agent != "" {
		in["agent"] = agent
	}
	if engine != "" {
		in["engine"] = engine
	}
	if supersedes > 0 {
		in["supersedes"] = supersedes
	}
	if len(entities) > 0 {
		in["entities"] = entities // server normalizes to slugs
	}
	var r struct {
		Block ContextBlock `json:"block"`
	}
	if err := c.do("POST", "/api/v1/threads/"+threadID+"/blocks", in, &r); err != nil {
		return nil, err
	}
	return &r.Block, nil
}

// ----- Common Ground: projects + graduation -----

type Project struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	Label     string `json:"label"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}

type ProjectBlock struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	Body          string `json:"body"`
	Author        string `json:"author"`
	Engine        string `json:"engine"`
	Status        string `json:"status"`
	GraduatedFrom int64  `json:"graduated_from"`
	CreatedAt     string `json:"created_at"`
}

func (c *Client) ListProjects() ([]Project, error) {
	var r struct {
		Projects []Project `json:"projects"`
	}
	if err := c.do("GET", "/api/v1/projects", nil, &r); err != nil {
		return nil, err
	}
	return r.Projects, nil
}

func (c *Client) CreateProject(label, orgSlug string) (*Project, error) {
	in := map[string]string{"label": label}
	if orgSlug != "" {
		in["org_slug"] = orgSlug
	}
	var r struct {
		Project Project `json:"project"`
	}
	if err := c.do("POST", "/api/v1/projects", in, &r); err != nil {
		return nil, err
	}
	return &r.Project, nil
}

// GetProject returns a project + its canon (graduated blocks).
func (c *Client) GetProject(id string) (*Project, []ProjectBlock, error) {
	var r struct {
		Project Project        `json:"project"`
		Blocks  []ProjectBlock `json:"blocks"`
	}
	if err := c.do("GET", "/api/v1/projects/"+id, nil, &r); err != nil {
		return nil, nil, err
	}
	return &r.Project, r.Blocks, nil
}

// AttachThreadProject attaches a thread to a project (so it inherits the project's canon).
func (c *Client) AttachThreadProject(threadID, projectID string) error {
	return c.do("POST", "/api/v1/threads/"+threadID+"/projects", map[string]string{"project_id": projectID}, nil)
}

// ReviewBlock accepts ("accept" → visible to agents) or rejects ("reject" → deleted) a scribe
// proposal on a thread.
func (c *Client) ReviewBlock(threadID string, blockID int64, action string) error {
	return c.do("POST", fmt.Sprintf("/api/v1/threads/%s/blocks/%d/review", threadID, blockID), map[string]string{"action": action}, nil)
}

// DistillParty runs the ambient scribe over a party's channel, proposing facts. Returns the
// count proposed (they land as 'proposed' in the party's linked thread, pending human review).
func (c *Client) DistillParty(partyID string) (int, error) {
	var r struct {
		Proposed int    `json:"proposed"`
		Skipped  string `json:"skipped"`
	}
	if err := c.do("POST", "/api/v1/parties/"+partyID+"/distill", nil, &r); err != nil {
		return 0, err
	}
	if r.Skipped != "" {
		return 0, fmt.Errorf("%s", r.Skipped)
	}
	return r.Proposed, nil
}

// GraduateBlock promotes a thread block into a project's canon (owner-gated server-side).
func (c *Client) GraduateBlock(threadID string, blockID int64, projectID string, supersedes int64) (*ProjectBlock, error) {
	in := map[string]any{"project_id": projectID}
	if supersedes > 0 {
		in["supersedes"] = supersedes
	}
	var r struct {
		Block ProjectBlock `json:"block"`
	}
	if err := c.do("POST", fmt.Sprintf("/api/v1/threads/%s/blocks/%d/graduate", threadID, blockID), in, &r); err != nil {
		return nil, err
	}
	return &r.Block, nil
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

// ActiveParty is an open party the logged-in user can join (surfaced by `ptln party`).
type ActiveParty struct {
	ID        string `json:"id"`
	Mode      string `json:"mode"`
	OrgSlug   string `json:"org_slug"`
	OrgName   string `json:"org_name"`
	CreatedAt string `json:"created_at"`
	IsMine    bool   `json:"is_mine"`
}

// ListParties returns the open parties the user's teams are running (no tokens — join mints one).
func (c *Client) ListParties() ([]ActiveParty, error) {
	var out struct {
		Parties []ActiveParty `json:"parties"`
	}
	return out.Parties, c.do("GET", "/api/v1/parties", nil, &out)
}

// JoinParty mints a fresh per-agent join link for a party the user can see (team member). label
// is a human hint stored with the token (e.g. the agent name).
func (c *Client) JoinParty(id, label string) (string, error) {
	var out struct {
		Link string `json:"link"`
	}
	err := c.do("POST", "/api/v1/parties/"+id+"/join", map[string]string{"label": label}, &out)
	return out.Link, err
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
	return p.postBody(in)
}

// PostMeta posts with a structured meta payload (e.g. a cited "position") alongside the
// body. The server whitelists what it stores from meta — it can't override to/avatar.
func (p *PartyClient) PostMeta(name, body, kind string, meta map[string]any) (int64, error) {
	in := map[string]any{"name": name, "body": body}
	if kind != "" {
		in["kind"] = kind
	}
	if len(meta) > 0 {
		in["meta"] = meta
	}
	return p.postBody(in)
}

// postBody POSTs a prepared message body with a 3-try transient-retry policy.
func (p *PartyClient) postBody(in map[string]any) (int64, error) {
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

// ----- daemon (Epic R remote-launch device transport; R1 = transport only) -----

// ErrDaemonRevoked is returned by DaemonStream when the device token no longer resolves an
// active daemon (revoked here or from the web) — terminal, so `daemon run` stops cleanly.
var ErrDaemonRevoked = fmt.Errorf("daemon device token revoked or invalid")

// RegisterDaemon enrols this machine as a launch daemon under the logged-in user and
// returns the daemon id + a DEVICE-scoped token (plt_dmn_…). Login-token auth (Client.do),
// so only the authenticated owner can enrol a device under their own account.
func (c *Client) RegisterDaemon(label string) (id, token string, err error) {
	var out struct {
		DaemonID    string `json:"daemon_id"`
		DeviceToken string `json:"device_token"`
	}
	if err := c.do("POST", "/api/v1/daemon/register", map[string]string{"device_label": label}, &out); err != nil {
		return "", "", err
	}
	return out.DaemonID, out.DeviceToken, nil
}

// LaunchEvent is a launch request pushed down the daemon stream. A "launch" (pending) event
// carries the LABEL only — a notification the owner may approve. An "accepted" event also
// carries the single-use PartyJoinRef (minted server-side at accept time) and is the
// execution trigger. Never a path, command, or raw party token — the daemon resolves the
// label against its OWN local registry before anything runs.
type LaunchEvent struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	PartyJoinRef string `json:"party_join_ref"`
	ProjectLabel string `json:"project_label"`
	Preset       string `json:"preset"`
	PartyID      string `json:"party_id"`
}

// RunEvent is a run-profile (O.2) pushed down the daemon stream: a REFERENCE — a project
// LABEL + thread + tasks + preset — never a path, command, or argv. A `run` event carries a
// QUEUED run (all the fields the daemon needs to reconcile locally); the daemon resolves the
// label against its OWN local registry (resolveRun) before anything runs, and the tasks reach
// crank only as DATA (a worklist file). Distinct from LaunchEvent: a run is a whole worklist,
// keyed by RunID (not RequestID), and needs no join-ref exchange (nothing to fetch server-side).
type RunEvent struct {
	Type         string   `json:"type"`
	RunID        string   `json:"run_id"`
	ProjectLabel string   `json:"project_label"`
	ThreadID     string   `json:"thread_id"`
	Tasks        []string `json:"tasks"`
	Preset       string   `json:"preset"`
	// MaxTokens is the run's optional token ceiling (#81 slice 3b). >0 → the daemon appends
	// --max-tokens N to crank's argv so the run pauses at the wall (slice 2); 0/absent = unbounded.
	MaxTokens int `json:"max_tokens"`
	// MergePolicy (#77 slice 3) is the per-run branch handling: manual (default) | pr | auto. The
	// daemon passes pr/auto through to crank (--merge-policy); manual is crank's no-op default.
	MergePolicy string `json:"merge_policy"`
}

// DaemonStream holds the daemon's outbound, long-lived SSE connection to the control plane
// using its DEVICE token (not the login token). onReady fires at `: ready`; onLaunch fires
// per PENDING request (a notification — no join ref — that the owner may approve); onAccepted
// fires when a request has become `accepted` (by ANY surface — the web modal or a CLI) and
// carries the single-use join ref, so it's the EXECUTION trigger (exchange+spawn); onRun fires
// per QUEUED run-profile (O.2) so the daemon can reconcile it and drive crank. onKill fires
// with a request id when a party member asks to stop that launch. Returns when the connection
// drops so the caller reconnects.
func DaemonStream(ctx context.Context, base, token string, onReady func(), onLaunch func(LaunchEvent), onAccepted func(LaunchEvent), onRun func(RunEvent), onKill func(string)) error {
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/daemon/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrDaemonRevoked
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("daemon stream: %s", res.Status)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == ": ready":
			if onReady != nil {
				onReady()
			}
		case strings.HasPrefix(line, "data: "):
			raw := line[6:]
			// A run event carries run_id + tasks (not request_id), so branch on the type
			// before the launch decode — otherwise the RequestID guard would drop it.
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(raw), &probe) != nil {
				continue
			}
			if probe.Type == "run" { // O.2 run-profile — reference, never a command
				var rev RunEvent
				if json.Unmarshal([]byte(raw), &rev) == nil && rev.RunID != "" && onRun != nil {
					onRun(rev)
				}
				continue
			}
			var ev LaunchEvent
			if json.Unmarshal([]byte(raw), &ev) != nil || ev.RequestID == "" {
				continue
			}
			switch ev.Type {
			case "launch": // pending notification (no ref)
				if onLaunch != nil {
					onLaunch(ev)
				}
			case "accepted": // authorized by some surface → carries the join ref → execute
				if onAccepted != nil {
					onAccepted(ev)
				}
			case "kill":
				if onKill != nil {
					onKill(ev.RequestID)
				}
			}
		}
	}
	return sc.Err()
}

// daemonDo is the device-token HTTP helper for the daemon's control-plane calls (mirror,
// list, decision, exchange). Bearer is the device token, not the login token.
func daemonDo(method, base, token, path string, in, out any) error {
	body := bytes.NewReader(nil)
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
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

// DaemonProjectRef is a mirrored launch target — LABEL + preset only, NEVER a path.
type DaemonProjectRef struct {
	Label  string `json:"label"`
	Preset string `json:"preset"`
}

// MirrorProjects (R2) replaces this daemon's advertised labels server-side. Paths cannot
// leak: the payload has no path field by construction.
func MirrorProjects(base, token string, projects []DaemonProjectRef) error {
	return daemonDo("PUT", base, token, "/api/v1/daemon/projects", map[string]any{"projects": projects}, nil)
}

// ListPendingLaunches (R3) fetches this daemon's pending requests (no live stream needed).
func ListPendingLaunches(base, token string) ([]LaunchEvent, error) {
	var out struct {
		Requests []LaunchEvent `json:"requests"`
	}
	return out.Requests, daemonDo("GET", base, token, "/api/v1/daemon/launch", nil, &out)
}

// SetLaunchStatus (R3) moves a request along its state machine from the daemon's side
// (accepted/declined/spawned/failed/killed). The server enforces legal transitions.
func SetLaunchStatus(base, token, requestID, status, detail string) error {
	in := map[string]any{"status": status}
	if detail != "" {
		in["detail"] = detail
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/launch/"+requestID, in, nil)
}

// SetRunStatus (O.2) moves a run along its lifecycle from the OWNING daemon's side
// (running/done/failed/declined/killed). Mirrors SetLaunchStatus; the server enforces legal
// transitions and that the run belongs to this daemon (device-token auth).
func SetRunStatus(base, token, runID, status, detail string) error {
	in := map[string]any{"status": status}
	if detail != "" {
		in["detail"] = detail
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID, in, nil)
}

// UpsertRunTask (O.3) reports one task's lifecycle transition to the per-TASK store, keyed by
// (run_id, idx). crank calls it as it processes the worklist — queued up front, then running
// before each task and done/failed after. Device-token auth (the owning daemon exposes its base
// + token to the crank child via env). Best-effort telemetry: crank logs + continues on error.
func UpsertRunTask(base, token, runID string, idx int, task, status, branch, detail string) error {
	in := map[string]any{"idx": idx, "status": status}
	if task != "" {
		in["task"] = task
	}
	if branch != "" {
		in["branch"] = branch
	}
	if detail != "" {
		in["detail"] = detail
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/tasks", in, nil)
}

// RunTaskStatus is one task's stored lifecycle state (O.3), keyed by idx within the run — the
// minimum crank --resume (#81 slice 3a) needs to skip already-`done` tasks without re-running them.
type RunTaskStatus struct {
	Idx    int    `json:"idx"`
	Status string `json:"status"`
}

// ListRunTasks (O.3) reads a run's per-task lifecycle rows (device-token GET), mirroring
// UpsertRunTask's auth + owning-daemon guard. crank --resume uses the `done` indices to skip
// finished work when re-invoked on a paused run. Best-effort at the call site: crank runs the
// full list if this fails.
func ListRunTasks(base, token, runID string) ([]RunTaskStatus, error) {
	var out struct {
		Tasks []RunTaskStatus `json:"tasks"`
	}
	if err := daemonDo("GET", base, token, "/api/v1/daemon/run/"+runID+"/tasks", nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// ClaimedTask is one task a worker atomically claimed from a run (#77 slice 1). Idx locates it in
// the worklist; Task is the DATA the worker runs; LeaseExpires is when an unfinished claim may be
// reclaimed by a peer (the worker should finish or renew before then).
type ClaimedTask struct {
	Idx          int    `json:"idx"`
	Task         string `json:"task"`
	LeaseExpires string `json:"lease_expires_at"`
}

// ClaimNextTask (#77 slice 1) atomically claims a run's next available task for THIS daemon so
// multiple workers can chew one run's worklist concurrently without collision (server-side
// FOR UPDATE SKIP LOCKED). Device-token auth, authorized by org membership (any daemon whose
// owner is in the run's org — the fleet). Returns nil (no error) when the pool is DRAINED, which
// is the worker's signal to stop looping.
func ClaimNextTask(base, token, runID string) (*ClaimedTask, error) {
	var out struct {
		Task *ClaimedTask `json:"task"`
	}
	if err := daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/claim", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Task, nil // nil = pool drained
}

// ExchangeJoinRef (R3) trades the single-use join ref for a freshly-minted party join link.
// Only valid once the request is `accepted`; the raw party token lives only in the response.
func ExchangeJoinRef(base, token, requestID, ref string) (link, label string, err error) {
	var out struct {
		Link  string `json:"link"`
		Label string `json:"label"`
	}
	if err := daemonDo("POST", base, token, "/api/v1/daemon/launch/"+requestID+"/exchange",
		map[string]any{"party_join_ref": ref}, &out); err != nil {
		return "", "", err
	}
	return out.Link, out.Label, nil
}

// CreateLaunchRequest (R3) is the REQUESTER side (login-token), used by the CLI dev trigger
// and, in R4, the web "Add agent" UI. Returns the new request id.
func (c *Client) CreateLaunchRequest(partyID, daemonID, label, preset string) (string, error) {
	var out struct {
		RequestID string `json:"request_id"`
	}
	err := c.do("POST", "/api/v1/daemon/launch",
		map[string]string{"party_id": partyID, "daemon_id": daemonID, "project_label": label, "preset": preset}, &out)
	return out.RequestID, err
}

// RevokeDaemon self-revokes this device's token (DELETE /daemon/register, device-token
// auth). Idempotent server-side, so disabling an already-revoked daemon is not an error.
func RevokeDaemon(base, token string) error {
	req, err := http.NewRequest("DELETE", base+"/api/v1/daemon/register", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("revoke: %s", res.Status)
	}
	return nil
}

// Transcript fetches the party's FULL discussion as a markdown transcript — the durable
// artifact behind the read_transcript MCP tool. Hits the messages endpoint with
// ?format=markdown, which serves reads even after a party has closed.
func (p *PartyClient) Transcript() (string, error) {
	req, err := http.NewRequest("GET", p.Base+"/api/v1/parties/"+p.ID+"/messages?format=markdown", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return "", fmt.Errorf("transcript: %s", res.Status)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		return "", err
	}
	return buf.String(), nil
}
