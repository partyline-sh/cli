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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func Base() string {
	v := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if v == "" {
		v = prodBase // production (env.go); PARTYLINE_API overrides for dev + self-hosting
	}
	return strings.TrimRight(v, "/")
}

// Scoped to the control plane this binary is pointed at — see env.go. Production is unchanged
// (~/.partyline/token); staging and local get their own, so dogfooding cannot overwrite a prod
// login.
func tokenPath() string {
	return filepath.Join(ConfigDir(), "token")
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
	return filepath.Join(ConfigDir(), "account.json")
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
// LatestVersion is the CLI's update check. It appends the caller's version + OS as anonymous query
// params (no id, no PII) so the control plane can count update-check volume by version/OS
// (Telemetry B). The check itself is already gated by the CLI's update-check opt-out, so an opted-
// out install never sends this.
func (c *Client) LatestVersion(cliVersion, goos string) (latest, minSupported, notice string, err error) {
	q := url.Values{}
	if cliVersion != "" {
		q.Set("v", cliVersion)
	}
	if goos != "" {
		q.Set("os", goos)
	}
	req, err := http.NewRequest("GET", c.Base+"/api/v1/version?"+q.Encode(), nil)
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
	ID string `json:"id"`
	// ProjectLabel is the project this thread's work belongs to, resolved server-side. It is the
	// machine-independent answer to "which project does this run in?" — the question that used to be
	// answered only by the session's working directory, which is wrong whenever a session is bound to
	// a thread for a repo it is not sitting in.
	ProjectLabel string `json:"-"`
	OrgID        string `json:"org_id"`
	Title        string `json:"title"`
	Visibility   string `json:"visibility"` // "private" | "team"
	CreatedBy    string `json:"created_by"`
	CreatedAt    string `json:"created_at"`
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

// ResolveThread returns the thread a PROJECT plans into (projects⊃threads): the server resolves the
// project's newest attached thread — creating it lazily, walking child→umbrella — so the CLI lands in
// the SAME thread as the web for the same repo. 404 if the label isn't a registered project.
func (c *Client) ResolveThread(projectLabel string) (*Thread, error) {
	var r struct {
		ThreadID string `json:"thread_id"`
		Title    string `json:"title"`
	}
	if err := c.do("POST", "/api/v1/threads/resolve", map[string]string{"project_label": projectLabel}, &r); err != nil {
		return nil, err
	}
	return &Thread{ID: r.ThreadID, Title: r.Title}, nil
}

// ResolveThreadForRepo answers the same question from a git remote instead of a project label —
// what `ptln thread bind` used to make a human answer by hand (#586).
//
// `create` is the read/write split, and it is the whole safety of the feature: false on a read
// (most repos on a machine are not partyline projects, and "no thread here" is a fine answer), true
// only when someone is deliberately recording a durable fact and needs somewhere to put it.
//
// An empty ThreadID with a nil error is the ORDINARY not-found — callers must treat it as "this repo
// has no shared context" and carry on, never as a failure.
func (c *Client) ResolveThreadForRepo(remote, name string, create bool) (*Thread, bool, error) {
	var r struct {
		ThreadID     string `json:"thread_id"`
		Title        string `json:"title"`
		ProjectLabel string `json:"project_label"`
		Created      bool   `json:"created"`
	}
	body := map[string]any{"remote": remote, "name": name, "create": create}
	if err := c.do("POST", "/api/v1/threads/resolve", body, &r); err != nil {
		return nil, false, err
	}
	if r.ThreadID == "" {
		return nil, false, nil
	}
	return &Thread{ID: r.ThreadID, Title: r.Title}, r.Created, nil
}

// ResolveRepo asks the control plane which project (and thread) this repo's `origin` remote already
// maps to. NEVER creates anything — it is the duplicate guard for `create_project`.
//
// The match lives on the SERVER because the server owns remote normalization (one repo cloned over
// ssh and over https is one repo). A second normalizer in Go would be a second thing to keep in
// step, and the failure when they drift is the one this call exists to prevent: a duplicate project
// for a repo the org already has.
//
// An empty label with a nil error is the ordinary "this repo is not a project yet".
func (c *Client) ResolveRepo(remote string) (projectLabel, threadID, title string, err error) {
	var r struct {
		ThreadID     string `json:"thread_id"`
		Title        string `json:"title"`
		ProjectLabel string `json:"project_label"`
	}
	body := map[string]any{"remote": remote, "create": false}
	if err := c.do("POST", "/api/v1/threads/resolve", body, &r); err != nil {
		return "", "", "", err
	}
	return r.ProjectLabel, r.ThreadID, r.Title, nil
}

// SetThreadProject sets threads.project_id — the field that decides WHICH THREAD A PROJECT RESOLVES
// TO. Distinct from AttachThreadProject, which writes the thread_projects join table and governs
// canon INHERITANCE. Two different links with two different meanings, and neither writes the other:
// attaching a thread to a project does not make the project resolve to it, which is why `ptln thread
// attach` could report success while the project went on resolving somewhere else entirely.
func (c *Client) SetThreadProject(threadID, projectID string) error {
	return c.do("PATCH", "/api/v1/threads/"+threadID, map[string]any{"project_id": projectID}, nil)
}

// ResolveThreadForProject answers "which context thread does this PROJECT use?" by label — the
// project-side twin of ResolveThreadForRepo. Never creates: this is a read for display, and a
// command that shows you something should not mint anything as a side effect.
//
// It exists because a project's thread was not visible from the CLI at all. You could see a
// project, and you could see a thread, and nothing connected the two — so when a repo resolved to
// the wrong thread there was no command that would show you.
func (c *Client) ResolveThreadForProject(label string) (*Thread, error) {
	var r struct {
		ThreadID string `json:"thread_id"`
		Title    string `json:"title"`
	}
	if err := c.do("POST", "/api/v1/threads/resolve", map[string]any{"project_label": label}, &r); err != nil {
		return nil, err
	}
	if r.ThreadID == "" {
		return nil, nil
	}
	return &Thread{ID: r.ThreadID, Title: r.Title}, nil
}

// SetProjectRepoURL stamps a project with the repo it belongs to, which is what makes the project
// findable from a checkout afterwards (see ResolveRepo). Without it a project is a label nothing can
// resolve back to.
func (c *Client) SetProjectRepoURL(id, repoURL string) error {
	return c.do("PATCH", "/api/v1/projects/"+id, map[string]any{"repo_url": repoURL}, nil)
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
		Thread       Thread         `json:"thread"`
		ProjectLabel string         `json:"project_label"`
		Blocks       []ContextBlock `json:"blocks"`
	}
	if err := c.do("GET", "/api/v1/threads/"+id, nil, &r); err != nil {
		return nil, nil, err
	}
	r.Thread.ProjectLabel = r.ProjectLabel
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

// Curate proposes a SYNTHESIZED brief for a thread (#78/#79): one coherent block that stands in for
// the atomic facts listed in absorbs. It lands as `proposed`, so it is invisible to recall and to
// the launch primer until a human accepts it — the #45 guarantee holds for a machine's summary
// exactly as it does for a scribe's proposal. Accepting is what retires the absorbed facts.
func (c *Client) Curate(threadID, body, agent, engine string, absorbs []int64, entities []string) (*ContextBlock, error) {
	in := map[string]any{"kind": "brief", "body": body, "propose": true}
	if agent != "" {
		in["agent"] = agent
	}
	if engine != "" {
		in["engine"] = engine
	}
	if len(absorbs) > 0 {
		in["absorbs"] = absorbs
	}
	if len(entities) > 0 {
		in["entities"] = entities
	}
	var r struct {
		Block ContextBlock `json:"block"`
	}
	if err := c.do("POST", "/api/v1/threads/"+threadID+"/blocks", in, &r); err != nil {
		return nil, err
	}
	return &r.Block, nil
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
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
	Label string `json:"label"`
	// RepoURL is the project's repo identity — what /threads/resolve matches an `origin` remote
	// against. Empty for a project created from the web without one.
	RepoURL   string `json:"repo_url"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	// ToolGrants (#574) — per-role agent tool grants: {"planning"|"build": {mcp, shell}}.
	ToolGrants map[string]ToolGrants `json:"agent_tool_grants"`
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

// UpdateProjectToolGrants replaces a project's per-role agent tool grants (#574). The server
// re-validates shape (cleanGrants) and rejects with an entry-naming error on a bad grant.
func (c *Client) UpdateProjectToolGrants(id string, grants map[string]ToolGrants) error {
	return c.do("PATCH", "/api/v1/projects/"+id, map[string]any{"agent_tool_grants": grants}, nil)
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

// PartyDoc reads a party's shared working document. The server creates it empty on first read,
// so a fresh party always answers with { body: "", version: n } rather than 404.
func (c *Client) PartyDoc(id string) (body string, version int, err error) {
	var out struct {
		Body    string `json:"body"`
		Version int    `json:"version"`
	}
	if e := c.do("GET", "/api/v1/parties/"+id+"/doc", nil, &out); e != nil {
		return "", 0, e
	}
	return out.Body, out.Version, nil
}

// SetPartyDoc replaces a party's document. baseVersion is the optimistic-concurrency token from
// PartyDoc — a stale one is refused server-side (409) rather than silently overwriting.
func (c *Client) SetPartyDoc(id, body string, baseVersion int) error {
	return c.do("PUT", "/api/v1/parties/"+id+"/doc", map[string]any{"body": body, "base_version": baseVersion}, nil)
}

// CreatePartyWithDoc creates a party and writes its document in one call, and is ATOMIC from the
// caller's point of view: if the document cannot be written the error is returned, so a caller that
// only wants the party as a RECORD (the CLI planning session, which files nothing without one)
// never mistakes a blank party for a written one.
func (c *Client) CreatePartyWithDoc(orgSlug, mode, doc string) (*CreatePartyOut, error) {
	p, err := c.CreateParty(orgSlug, mode)
	if err != nil {
		return nil, err
	}
	_, version, err := c.PartyDoc(p.ID)
	if err != nil {
		return nil, fmt.Errorf("party %s created but its document could not be read: %w", p.ID, err)
	}
	if err := c.SetPartyDoc(p.ID, doc, version); err != nil {
		return nil, fmt.Errorf("party %s created but its document could not be written: %w", p.ID, err)
	}
	return p, nil
}

// ---- the tools an agent needs to operate partyline without a browser --------------------------

// BoardCard is one card as the work board renders it.
type BoardCard struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Task    string `json:"task"`
	Column  string `json:"column"`
	Status  string `json:"status"`
	Machine string `json:"machine"`
	PRURL   string `json:"prUrl"`
	Failed  bool   `json:"failed"`
}

type Board struct {
	Backlog  []BoardCard `json:"backlog"`
	Building []BoardCard `json:"building"`
	Blocked  []BoardCard `json:"blocked"`
	Review   []BoardCard `json:"review"`
	Shipped  []BoardCard `json:"shipped"`
}

// RunsWorkingHere counts the runs still in flight on ONE machine, by asking the control plane rather
// than a local registry.
//
// It exists because `ptln update` restarts the daemon, and it runs as a SEPARATE process from the
// daemon — so it cannot see the daemon's in-process child registry (runsInFlight). The auto-update
// path checks that registry and defers while work is running; the manual path had no way to ask, and
// so restarted the daemon out from under live runs.
func (c *Client) RunsWorkingHere(daemonID string) (int, error) {
	if daemonID == "" {
		return 0, nil
	}
	board, err := c.ReadBoard()
	if err != nil {
		return 0, err
	}
	// The board names the MACHINE, not the daemon id, so resolve this daemon's label from the machine
	// list. Both are existing endpoints — widening the board payload for one caller would be the
	// wrong trade.
	machines, merr := c.MachineOffers()
	if merr != nil {
		return 0, merr
	}
	label := ""
	for _, m := range machines {
		if m.DaemonID == daemonID {
			label = m.Machine
			break
		}
	}
	if label == "" {
		return 0, nil // this machine is not in the caller's fleet — nothing we can attribute to it
	}
	// Building is the column that means "a worker is on it". Blocked and review are finished states a
	// restart cannot disturb.
	n := 0
	for _, card := range board.Building {
		if card.Machine == label {
			n++
		}
	}
	return n, nil
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

// MachineOffer is one machine and the directories it advertises, by opaque handle.
type MachineOffer struct {
	DaemonID string `json:"daemon_id"`
	Machine  string `json:"machine"`
	Online   bool   `json:"online"`
	Repos    []struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
		Parent string `json:"parent"`
	} `json:"repos"`
	Destinations []struct {
		Handle string `json:"handle"`
		Parent string `json:"parent"`
		Label  string `json:"label"`
	} `json:"destinations"`
}

// MachineOffers lists the caller's machines and what each can be pointed at.
func (c *Client) MachineOffers() ([]MachineOffer, error) {
	var r struct {
		Machines []MachineOffer `json:"machines"`
	}
	if err := c.do("GET", "/api/v1/daemon/candidates", nil, &r); err != nil {
		return nil, err
	}
	return r.Machines, nil
}

// BindMachineProject points a machine at a directory it advertised and registers it under `label`.
//
// handle names a repo the machine already has; destinationHandle names a directory to clone INTO
// (which needs the project to carry a repo_url). policy is the run mode — "auto" runs dispatched
// work unattended, "ask" queues it for the owner; "" leaves the machine's current grant alone, which
// is the only safe default for a call that may only have meant to bind a directory.
//
// REFERENCE-NOT-COMMAND: only handles the machine itself minted and advertised are accepted, so this
// can never point a daemon at a path it did not offer.
func (c *Client) BindMachineProject(daemonID, handle, destinationHandle, label, preset, engine, policy string) error {
	body := map[string]any{"label": label}
	if handle != "" {
		body["handle"] = handle
	}
	if destinationHandle != "" {
		body["destination_handle"] = destinationHandle
	}
	if preset != "" {
		body["preset"] = preset
	}
	if engine != "" {
		body["engine"] = engine
	}
	if policy != "" {
		body["policy"] = policy
	}
	return c.do("POST", "/api/v1/daemon/"+daemonID+"/bind-repo", body, nil)
}

// CloseParty ends a party (status → closed), making its token inert and moving it into History.
//
// Its reason for existing is CLEANUP: a caller that creates a party as the container for something
// it is about to write needs to be able to take it back when that write fails, or every failed
// attempt leaves a live, empty party behind. Best-effort by nature — the caller is already on an
// error path, and failing to tidy up must never replace the real error with a worse one.
func (c *Client) CloseParty(id string) error {
	return c.do("POST", "/api/v1/parties/"+id+"/close", nil, nil)
}

func (c *Client) OrgMembers(slug string) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	return out.Members, c.do("GET", "/api/v1/orgs/"+slug+"/members", nil, &out)
}

// ClaimInvites places this account in any team that invited its VERIFIED email — the CLI half of
// auto-claim, so someone who signed up without following the invite link (or was invited after
// signing up) still lands in their team. Returns the team name when it joined one.
func (c *Client) ClaimInvites() (claimed int, team string, err error) {
	var out struct {
		Claimed int `json:"claimed"`
		Org     *struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"org"`
	}
	if err := c.do("POST", "/api/v1/me/claim-invites", map[string]any{}, &out); err != nil {
		return 0, "", err
	}
	if out.Org != nil {
		team = out.Org.Name
	}
	return out.Claimed, team, nil
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

// ----- ask_peer (P0.b) — consult a teammate's daemon from a session via the MCP tools -----

// Peer is a consultable machine: a teammate's (or your own) daemon and the project labels it
// advertises. Same shape as a launch target — a peer IS a daemon in partyline's model.
type Peer struct {
	DaemonID    string   `json:"daemon_id"`
	DeviceLabel string   `json:"device_label"`
	Online      bool     `json:"online"`
	Version     string   `json:"version"`
	Projects    []string `json:"projects"`
}

// ListPeers returns the daemons the caller may consult (their org's members' machines + what each
// advertises). User-token authed; RLS-scoped server-side — never shows a machine the caller can't see.
func (c *Client) ListPeers() ([]Peer, error) {
	var out struct {
		Targets []Peer `json:"targets"`
	}
	if err := c.do("GET", "/api/v1/targets", nil, &out); err != nil {
		return nil, err
	}
	return out.Targets, nil
}

// AskPeer opens a consult against a target daemon for read-only feedback on a plan/question, scoped
// to an advertised label. Returns the consult id (the async handle) — poll GetConsult for the answer.
// No from_daemon: the MCP tool polls the handle rather than routing the answer to a resident daemon.
func (c *Client) AskPeer(targetDaemon, label, question string) (string, error) {
	var out struct {
		ConsultID string `json:"consult_id"`
	}
	in := map[string]string{"target_daemon": targetDaemon, "project_label": label, "question": question}
	if err := c.do("POST", "/api/v1/daemon/consult", in, &out); err != nil {
		return "", err
	}
	return out.ConsultID, nil
}

// ConsultResult is the current state of a consult handle: status ∈ pending/delivered/answered/
// declined/timed_out/failed, plus the answer once answered (or a detail on a terminal non-answer).
type ConsultResult struct {
	Status       string `json:"status"`
	ProjectLabel string `json:"project_label"`
	Answer       string `json:"answer"`
	Detail       string `json:"detail"`
}

// GetConsult polls a consult handle. Scoped to the asker server-side (from_user), so it only ever
// resolves a handle the caller opened.
func (c *Client) GetConsult(id string) (*ConsultResult, error) {
	var out ConsultResult
	if err := c.do("GET", "/api/v1/daemon/consult/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConsultCancelResult is what a withdrawal reports back. Cancelled=false is not a failure: it means
// the consult had already stopped moving (answered, declined, timed out) and Status says which — the
// asker asked to take back something that had already come back, and the useful reply is what happened.
type ConsultCancelResult struct {
	OK        bool   `json:"ok"`
	Cancelled bool   `json:"cancelled"`
	Status    string `json:"status"`
}

// CancelConsult withdraws a consult the caller ASKED. Scoped to the asker server-side (from_user), the
// same wall GetConsult uses, so a handle that isn't yours is indistinguishable from one that never
// existed — both are the same 404 with the same body, and this returns the error verbatim rather than
// guessing which it was.
func (c *Client) CancelConsult(id string) (*ConsultCancelResult, error) {
	var out ConsultCancelResult
	if err := c.do("POST", "/api/v1/daemon/consult/"+id+"/cancel", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Consult is one OPEN consult in either direction, as the list endpoint returns it. Inbound = a peer
// is waiting on one of MY machines to answer; outbound = an ask of mine still out with a peer.
// DaemonID is the daemon that must ANSWER it — for an inbound consult that is the machine the
// question was addressed to, which is how a client knows whether it can be answered HERE.
type Consult struct {
	Direction    string    `json:"direction"` // inbound | outbound
	ConsultID    string    `json:"id"`
	Status       string    `json:"status"`
	ProjectLabel string    `json:"project_label"`
	Question     string    `json:"question"`
	Peer         string    `json:"peer"`         // inbound: who asked · outbound: the device we asked
	DaemonID     string    `json:"daemon_id"`    // the daemon that answers
	DeviceLabel  string    `json:"device_label"` // its label — "answer this on <device>"
	CreatedAt    time.Time `json:"created_at"`
}

// ListConsults lists the caller's OPEN consults. direction ∈ inbound|outbound|all. User-token
// authed and read-only: ownership is proven server-side (a consult addressed to a daemon the caller
// doesn't own is never returned), and nothing here can answer — answering is the daemon's device-token
// route. This is the list that was missing: without it the answering side could only learn of a
// question from a live stream push, so a daemon restart lost every pending consult.
func (c *Client) ListConsults(direction string) ([]Consult, error) {
	var out struct {
		Consults []Consult `json:"consults"`
	}
	if err := c.do("GET", "/api/v1/daemon/consults?direction="+url.QueryEscape(direction), nil, &out); err != nil {
		return nil, err
	}
	return out.Consults, nil
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
	// Host is the hosting person's display name, resolved server-side. Absent from an older control
	// plane, which is why every consumer must fall back to the join code rather than show a blank.
	Host string `json:"host,omitempty"`
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

// InviteResult is the honest accounting of one invite request: who an invite actually
// reached (Accepted, named by the server — NOT inferred by subtracting the failures from
// what we submitted, which silently turns a capped, deduped or failed send into a success)
// and which targets resolved to nobody (Unresolved). Anything the caller submitted that
// appears in neither list was not delivered and must not be reported as if it were.
type InviteResult struct {
	Sent       int      `json:"sent"`       // invites the control plane delivered
	Armed      int      `json:"armed"`      // planned session: rows stored, delivery fires on go-live
	Accepted   []string `json:"accepted"`   // targets an invite reached (or was armed for)
	Unresolved []string `json:"unresolved"` // targets that resolved to nobody
}

// N is the number to report to a human: delivered, or armed on a session that isn't live yet.
func (r InviteResult) N() int {
	if r.Sent > 0 {
		return r.Sent
	}
	return r.Armed
}

// InviteSession invites by email address or @handle.
func (c *Client) InviteSession(id string, targets []string) (InviteResult, error) {
	var out InviteResult
	if err := c.do("POST", "/api/v1/sessions/"+id+"/invites",
		map[string]any{"targets": targets}, &out); err != nil {
		return InviteResult{}, err
	}
	return out, nil
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
	// Engine/Model are what THIS runner resolved for itself, reported alongside name/role on the
	// stream so the web can say what's actually answering. Display-only, and set only by the party
	// runner — every other PartyClient user leaves them empty and simply reports nothing.
	Engine string
	Model  string
}

// PartyInfo is the party's mode template — the system preamble to inject into the
// agent + the settings the runner reads (model, agent-turn brake). CLI flags override.
type PartyInfo struct {
	Mode         string `json:"mode"`
	SystemPrompt string `json:"system_prompt"`
	Settings     struct {
		Model         string `json:"model"`
		MaxAgentTurns *int   `json:"maxAgentTurns"` // pointer: distinguish 0 (off) from absent
		// Behavior envelope — server-authoritative party policy, so the common UX tweaks are a
		// control-plane deploy, not a daemon release. Pointers/typed values with a validated
		// daemon-side default when absent (older backend → the daemon's own defaults still apply).
		Grounded       *bool  `json:"grounded"`       // cited-position (evidence) mode. nil → mode-based default
		IdleTimeoutSec *int   `json:"idleTimeoutSec"` // kill a turn after N s of no output. nil → daemon default
		MaxTimeoutSec  *int   `json:"maxTimeoutSec"`  // absolute per-turn backstop. nil → daemon default
		ToolPosture    string `json:"toolPosture"`    // "read_only"(default)|"read_write" — bounded ENUM, never a raw tool list
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
// Retries transient failures (network errors + 5xx, incl. the proxy's
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

// DownloadAttachment fetches a message attachment's bytes (party-token auth) and writes them to
// destPath. Used when a human attaches files to a chat message: the runner pulls them local so the agent
// can Read them during its turn. Size-capped defensively; best-effort (a failed pull just omits the file).
func (p *PartyClient) DownloadAttachment(attID, destPath string) error {
	req, err := http.NewRequest("GET", p.Base+"/api/v1/parties/"+p.ID+"/attachments/"+attID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("download: %s", res.Status)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(res.Body, 30*1024*1024)) // matches the 25MB upload cap, with headroom
	return err
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

// httpDo performs ONE request attempt: builds the request with party-token auth (and a
// JSON Content-Type when body is non-nil), and hands back the raw response for the caller
// to classify and close. The single-shot primitive both doOnce paths and the retry loop
// share, so request construction lives in exactly one place.
func (p *PartyClient) httpDo(method, url string, body []byte, timeout time.Duration) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	return (&http.Client{Timeout: timeout}).Do(req)
}

// doWithRetry issues a request under the shared 3-try transient-retry policy: up to 3
// attempts, sleeping 1s then 2s between them, retrying network/TLS errors and 5xx
// responses; any other response (2xx or a 4xx client error) is returned immediately for
// the caller to classify. The caller owns the returned body. body is re-read on every
// attempt (fresh reader per try), so it is only safe for IDEMPOTENT requests — reads and
// the human-gated doc edit, never plan writes. Extracted from postBody; its policy here is
// byte-identical to postBody's original inline loop.
func (p *PartyClient) doWithRetry(method, url string, body []byte, timeout time.Duration) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		res, err := p.httpDo(method, url, body, timeout)
		if err != nil {
			lastErr = err // network/TLS — retry
			continue
		}
		if res.StatusCode >= 500 {
			res.Body.Close()
			lastErr = fmt.Errorf("server %s", res.Status) // transient — retry
			continue
		}
		return res, nil // 2xx or 4xx client error — caller classifies & closes
	}
	return nil, lastErr
}

// postBody POSTs a prepared message body with a 3-try transient-retry policy.
func (p *PartyClient) postBody(in map[string]any) (int64, error) {
	b, _ := json.Marshal(in)
	res, err := p.doWithRetry("POST", p.Base+"/api/v1/parties/"+p.ID+"/post", b, 20*time.Second)
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
		return 0, fmt.Errorf("%s", e.Error) // client error — do not retry
	}
	var out struct {
		ID int64 `json:"id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out.ID, nil
}

// PartyActivityLine is one line of an agent's LIVE STEP OUTPUT during a party turn (S1) — the party
// twin of RunLogLine. Streamed to the activity feed and tailed live over Realtime, then collapsed when
// the final message lands. Ephemeral telemetry, NOT part of the transcript. Seq is a producer-assigned
// ordering hint (one runner process per turn), not a security artifact.
type PartyActivityLine struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"` // "step" | "stdout" | "stderr"
	Body   string `json:"body"`
}

// AppendActivity posts a BATCH of the agent's step-output lines to the party activity feed (party-token
// auth). Best-effort telemetry — a dropped batch just means a few missing activity lines, never a failed
// turn — so NO retry (unlike Post, which is the agent's actual voice). Skips empty batches.
func (p *PartyClient) AppendActivity(name string, lines []PartyActivityLine) error {
	if len(lines) == 0 {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"name": name, "lines": lines})
	req, err := http.NewRequest("POST", p.Base+"/api/v1/parties/"+p.ID+"/activity", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// GetDoc fetches the party's shared working doc (EPIC A1). Best-effort — the runner
// injects it into the wake prompt, so an error just means "no doc this turn".
func (p *PartyClient) GetDoc() (body string, version int, err error) {
	// A doc read is idempotent — retry transient failures so a blip doesn't cost the agent a turn.
	res, err := p.doWithRetry("GET", p.Base+"/api/v1/parties/"+p.ID+"/doc", nil, 15*time.Second)
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
	// Retry transient failures like Post does. A retried edit inherits Post's accepted duplicate
	// risk — a doubled *pending* edit card, which is human-gated before it merges, so harmless.
	res, err := p.doWithRetry("POST", p.Base+"/api/v1/parties/"+p.ID+"/doc/propose", b, 20*time.Second)
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

// ----- plan (party planning tree: epics ▸ features ▸ tasks) -----

// PlanItem is one node of the party's planning tree (GET /plan contract). Readiness is
// deliberately untyped (any): the contract doesn't pin string vs numeric score, so we
// decode whatever the server sends and render it verbatim.
type PlanItem struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"` // "epic" | "feature" | "task"
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	Readiness     any        `json:"readiness"`
	ReadinessNote string     `json:"readiness_note"`
	HasRun        bool       `json:"has_run"`
	Children      []PlanItem `json:"children"`
}

// PlanTree is the party's full planning tree plus the thread it belongs to.
type PlanTree struct {
	ThreadID    string     `json:"thread_id"`
	ThreadTitle string     `json:"thread_title"`
	Tree        []PlanItem `json:"tree"`
}

// planDo is the party-token HTTP helper for the plan endpoints: path is relative to
// /api/v1/parties/<id>/plan. Error bodies are {error} JSON, surfaced as plain errors
// (same policy as ProposeEdit). Retry policy branches by operation: the plan READ (GET, the
// only idempotent plan op, backing plan_read) retries transient failures like the doc reads;
// the plan WRITES (POST/PATCH — plan_upsert/plan_move/plan_propose) stay single-shot, because
// plan writes are not idempotent-safe like Post and a retried mutation could double an item.
func (p *PartyClient) planDo(method, path string, in, out any) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	url := p.Base + "/api/v1/parties/" + p.ID + "/plan" + path
	var res *http.Response
	var err error
	if method == http.MethodGet {
		res, err = p.doWithRetry(method, url, body, 20*time.Second)
	} else {
		res, err = p.httpDo(method, url, body, 20*time.Second)
	}
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

// partyGet is a read of any party subresource. planDo hardcodes "/plan"; these reads sit beside it
// rather than under it. GET-only and retried (doWithRetry) — a transient failure on a read must not
// look to the agent like a missing capability, which is the whole class of bug this pair addresses.
func (p *PartyClient) partyGet(sub string, out any) error {
	res, err := p.doWithRetry(http.MethodGet, p.Base+"/api/v1/parties/"+p.ID+sub, nil, 20*time.Second)
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
	return json.NewDecoder(res.Body).Decode(out)
}

// SwitchContext re-points this party at a different persona and/or project WITHOUT ending the
// conversation (POST /context). Either field may be empty to leave it unchanged.
//
// Single-shot, not retried: this is a state change, and a retry after an ambiguous timeout could
// announce a second switch into the transcript. A failed switch is safe — the agent keeps the rules
// it had — so failing loudly beats retrying blind.
func (p *PartyClient) SwitchContext(mode, projectLabel string) (string, error) {
	in := map[string]any{}
	if mode != "" {
		in["mode"] = mode
	}
	if projectLabel != "" {
		in["project_label"] = projectLabel
	}
	body, _ := json.Marshal(in)
	res, err := p.httpDo(http.MethodPost, p.Base+"/api/v1/parties/"+p.ID+"/context", body, 20*time.Second)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Mode         string `json:"mode"`
		ProjectLabel string `json:"project_label"`
		Applies      string `json:"applies"`
		Error        string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode >= 400 {
		if out.Error == "" {
			out.Error = res.Status
		}
		return "", fmt.Errorf("%s", out.Error)
	}
	msg := "Context switched. Persona: " + out.Mode
	if out.ProjectLabel != "" {
		msg += " · project: " + out.ProjectLabel
	}
	if out.Applies != "" {
		msg += " (applies " + out.Applies + ")"
	}
	return msg, nil
}

// Capabilities fetches the LIVE capability manifest (GET /capabilities). The same text rides the
// system prompt at launch; re-reading it picks up a grant added mid-conversation, which a prompt
// cannot.
func (p *PartyClient) Capabilities() (string, error) {
	var out struct {
		Manifest string `json:"manifest"`
	}
	if err := p.partyGet("/capabilities", &out); err != nil {
		return "", err
	}
	return out.Manifest, nil
}

// Backlog is the team's Build backlog + in-flight runs, summarized (GET /backlog). Titles and
// counts only — enough to plan against, not enough to page through every run.
type Backlog struct {
	Queued []struct {
		Title string `json:"title"`
	} `json:"queued"`
	Building []struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"building"`
	NeedsAttention []struct {
		Title  string `json:"title"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"needs_attention"`
	RecentlyShipped []struct {
		Title string `json:"title"`
	} `json:"recently_shipped"`
	Totals map[string]int `json:"totals"`
}

func (p *PartyClient) Backlog() (*Backlog, error) {
	var out Backlog
	if err := p.partyGet("/backlog", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Fleet is the team's machines and their health (GET /fleet) — what backlog_read is to the work,
// this is to the machines the work runs on.
//
// EVERY health field is a POINTER on purpose. A node that has never reported memory omits the key
// entirely, and a pointer is the only way Go can tell "not reported" from "reported as zero" after
// json.Unmarshal — decoding into a float64 would turn silence into "0% used, 0 GB free", which is a
// measurement the renderer would print and an agent would believe.
type FleetLoad struct {
	Load1 *float64 `json:"load1"`
	Cores *int     `json:"cores"`
}

type FleetMemory struct {
	UsedPct *float64 `json:"used_pct"`
	TotalMB *float64 `json:"total_mb"`
}

type FleetDisk struct {
	UsedPct *float64 `json:"used_pct"`
	FreeGB  *float64 `json:"free_gb"`
	Volume  string   `json:"volume"` // a short volume label ("Data", "/"), never a mount path
}

type FleetSessions struct {
	Live    int      `json:"live"`
	Busy    int      `json:"busy"`
	Engines []string `json:"engines"` // "claude opus", "codex" — pairs in play, not session titles
}

type FleetBuilding struct {
	Task    string `json:"task"`
	Project string `json:"project"`
	Count   int    `json:"count"` // runs building HERE right now; Task names only the longest-running
}

type FleetNodeInfo struct {
	ID        string         `json:"id"` // matched against this machine's own device.json to mark "here"
	Name      string         `json:"name"`
	Owner     string         `json:"owner"`
	Online    bool           `json:"online"`
	Status    string         `json:"status"`
	LastSeenS *int           `json:"last_seen_s"` // nil = never checked in (NOT "0 seconds ago")
	Version   string         `json:"version"`
	Projects  []string       `json:"projects"`
	Building  *FleetBuilding `json:"building"`
	Load      *FleetLoad     `json:"load"`
	Memory    *FleetMemory   `json:"memory"`
	Disk      *FleetDisk     `json:"disk"`
	Sessions  *FleetSessions `json:"sessions"`
}

type Fleet struct {
	Nodes []FleetNodeInfo `json:"nodes"`
	Count int             `json:"count"`
}

func (p *PartyClient) Fleet() (*Fleet, error) {
	var out Fleet
	if err := p.partyGet("/fleet", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PlanRead fetches the party's planning tree (GET /plan).
func (p *PartyClient) PlanRead() (*PlanTree, error) {
	var out PlanTree
	if err := p.planDo("GET", "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PlanCreateItem creates a plan item (POST /plan/items) and returns its id. fields is the
// contract body (kind, title, document?, acceptance_criteria?, readiness?, readiness_note?,
// parent_id?) — the caller whitelists the keys. Agent-created items land as drafts server-side.
func (p *PartyClient) PlanCreateItem(fields map[string]any) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := p.planDo("POST", "/items", fields, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// PlanPatchItem edits a plan item (PATCH /plan/items/<id>). fields carries only the keys to
// change (title, document, acceptance_criteria, readiness, readiness_note); the server
// requires readiness_note whenever readiness changes, and refuses building/done items and
// the session's own ## Plan subtree.
func (p *PartyClient) PlanPatchItem(itemID string, fields map[string]any) error {
	return p.planDo("PATCH", "/items/"+itemID, fields, nil)
}

// PlanMove reparents/reorders a plan item (POST /plan/items/<id>/move). parentID nil → JSON
// null = move to the top level; rank nil = let the server keep/choose the position.
func (p *PartyClient) PlanMove(itemID string, parentID *string, rank *float64) error {
	in := map[string]any{"parent_id": parentID}
	if rank != nil {
		in["rank"] = *rank
	}
	return p.planDo("POST", "/items/"+itemID+"/move", in, nil)
}

// PlanPropose asks a HUMAN to promote a task to the Build backlog or archive a dead item
// (POST /plan/propose). Nothing executes until a person approves.
func (p *PartyClient) PlanPropose(action, itemID, note string) error {
	in := map[string]any{"action": action, "item_id": itemID}
	if note != "" {
		in["note"] = note
	}
	return p.planDo("POST", "/propose", in, nil)
}

// WorkSearch asks whether the org has ALREADY planned something like this (POST /work-search) —
// the read behind the MCP `search_work_items` tool, so a describe session flags a duplicate instead
// of specifying one. Org scope comes from the party row server-side; the query is all we send.
//
// A POST, but a pure READ: retried like the other reads, because a transient blip here reads to an
// agent as "no duplicates exist", which is the exact wrong answer.
type WorkMatch struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	Title     string  `json:"title"`
	ThreadID  string  `json:"thread_id"`
	Readiness int     `json:"readiness"`
	Snippet   string  `json:"snippet"`
	Rank      float64 `json:"rank"`
}

type WorkSearchResult struct {
	Query   string      `json:"query"`
	Matches []WorkMatch `json:"matches"`
}

func (p *PartyClient) SearchWork(query string) (*WorkSearchResult, error) {
	body, _ := json.Marshal(map[string]any{"query": query})
	res, err := p.doWithRetry(http.MethodPost, p.Base+"/api/v1/parties/"+p.ID+"/work-search", body, 20*time.Second)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%s", e.Error)
	}
	var out WorkSearchResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
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
	// Appended only when known, so an older server that ignores them and a runner that has nothing
	// to say produce the same request as before.
	if p.Engine != "" {
		u += "&engine=" + url.QueryEscape(p.Engine)
	}
	if p.Model != "" {
		u += "&model=" + url.QueryEscape(p.Model)
	}
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
	// A backlog read is idempotent — retry transient failures with the shared 3-try policy
	// (up to 3 attempts, 1s then 2s waits). Stream can't reuse doWithRetry (it's a long-lived
	// SSE connection, not a request/response), so the loop lives here. `ready` distinguishes a
	// clean finish (": ready" replayed → cancel) from a drop BEFORE the backlog arrived: only
	// the latter is a transient failure worth retrying. Never-ready after 3 tries surfaces the
	// error (callers already handle it), but a normal read still returns whatever arrived.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		cctx, cancel := context.WithCancel(ctx)
		var msgs []PartyMsg
		ready := false
		err := p.Stream(cctx, name, "", 0,
			func() { ready = true; cancel() }, // backlog fully replayed → stop reading and disconnect
			func(m PartyMsg) { msgs = append(msgs, m) },
		)
		cancel()
		if err == ErrPartyClosed {
			return nil, err
		}
		if ready {
			return msgs, nil // clean finish — the ctx-cancel "error" is expected, not a failure
		}
		lastErr = err // dropped before the backlog replayed — transient, retry
	}
	return nil, lastErr
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
	// machine_id lets the server RE-REGISTER IN PLACE instead of minting a second row for the same
	// machine (which strands every run pinned to the old daemon_id). Empty on platforms that cannot
	// supply a stable id — the server then falls back to the historical insert.
	body := map[string]string{"device_label": label}
	if mid := MachineID(); mid != "" {
		body["machine_id"] = mid
	}
	if err := c.do("POST", "/api/v1/daemon/register", body, &out); err != nil {
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
	// Engine (optional) is the project's server-configured PLANNING engine for this launch
	// ("claude"|"codex"|"gemini"|"antigravity"). Reference-not-command: resolveLaunch validates
	// it against the local engines registry before it reaches the argv; empty/absent/unknown =
	// the local registry's per-project engine (the pre-field behavior).
	Engine string `json:"engine,omitempty"`
	// ToolGrants (optional, #574) are the org's per-project tool grants for planning agents:
	// MCP server NAMES and shell command PREFIXES only — pure DATA. The daemon re-validates
	// the shapes and resolves names against its OWN local catalog (resolveLaunchGrants);
	// nothing in here is ever a path, command, or argv. Absent = today's read-only posture.
	ToolGrants *ToolGrants `json:"tool_grants,omitempty"`
}

// ToolGrants is the wire shape of #574 agent-tool grants (projects.agent_tool_grants, one role).
type ToolGrants struct {
	MCP   []string `json:"mcp,omitempty"`
	Shell []string `json:"shell,omitempty"`
}

// DaemonOwner returns the handle + email of the account that OWNS this daemon (device-token auth,
// GET /api/v1/daemon/whoami). `ptln daemon status` uses it to warn when the daemon is owned by a
// different account than the one currently logged in — the silent-orphan footgun that leaves a
// daemon invisible in the new account's fleet.
func DaemonOwner(base, deviceToken string) (handle, email string, err error) {
	var out struct {
		Handle string `json:"handle"`
		Email  string `json:"email"`
	}
	if err = daemonDo("GET", base, deviceToken, "/api/v1/daemon/whoami", nil, &out); err != nil {
		return "", "", err
	}
	return out.Handle, out.Email, nil
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
	// MaxPerMachine is the team's JOBS PER MACHINE cap, resolved server-side. The WEB is still the
	// authority — it withholds runs beyond the cap across the whole fleet — but the daemon needs the
	// number so its own worker pool isn't a tighter, invisible second cap. 0 = unset/unlimited, in
	// which case the daemon falls back to PARTYLINE_RUN_WORKERS (default 1, the old behaviour).
	MaxPerMachine int `json:"max_per_machine"`
	// MaxRepairRounds (#569) is the project's per-run bound on the in-run repair loop (builder fixes
	// → gate re-reviews) — projects.max_repair_rounds, 0–5. A POINTER because 0 is a meaningful value
	// ("quarantine on the first rejection, don't retry"), so it can't double as absent: nil/absent
	// (the column is NULL) = crank's own default. Reference-not-command: a plain int the daemon
	// re-validates before it reaches --max-repairs.
	MaxRepairRounds *int `json:"max_repair_rounds,omitempty"`
	// MergePolicy (#77 slice 3) is the per-run branch handling: manual (default) | pr | auto. The
	// daemon passes pr/auto through to crank (--merge-policy); manual is crank's no-op default.
	MergePolicy string `json:"merge_policy"`
	// ReviewRequired: the org's review gate is ON for this run, so crank opens the PR as a DRAFT (the
	// human's web Accept marks it ready). DATA — a plain boolean the server sets; absent/false = a
	// normal PR (no review step to un-draft it). Old daemons ignore it.
	ReviewRequired bool `json:"review_required,omitempty"`
	// Go marks this a WEB-ACCEPTED run (B): the owner clicked Start in the web, the server flipped
	// the run to `accepted`, and the stream re-pushed it with go=true as the EXECUTION trigger —
	// the run-profile twin of a launch's `accepted` event. On a go event the daemon runs crank
	// immediately, no console approve-run needed (so an always-on/headless daemon can run web work).
	// A plain (go=false) run event is still just a notification the owner may approve at the console.
	Go bool `json:"go"`
	// Restart (board/detail "Restart" CTA) marks a re-dispatch that should START OVER: the daemon
	// passes crank `--restart` INSTEAD of `--resume`, so each task gets a fresh worktree+branch and
	// prior done/blocked state is ignored. Default false = the normal resume-from-where-it-stopped
	// behaviour. One-shot: the server clears restart_requested on the run's `running` transition.
	Restart bool `json:"restart"`
	// Globals (Phase B3) is the project's document — its rules/stack/guardrails (projects.document).
	// The server includes it on the run event so the daemon can write it into each task's WORKTREE as
	// the worktree globals files (a repo-root file wouldn't reach the worktree the worker runs in). Empty = the
	// project has no document. Still reference-not-command: it's inert context, never a command.
	Globals string `json:"globals,omitempty"`
	// AnchoredContext is the thread's recorded knowledge about the FILES this run's tasks name —
	// resolved server-side by matching the task text against each fact's code anchors (#135).
	//
	// Deliberately separate from Globals. Globals are the project's standing rules and are the same
	// for every task; this changes per task and is the specific history of the code being touched.
	// Keeping them apart is also what lets us tell which of the two is worth its tokens.
	AnchoredContext string `json:"anchored_context,omitempty"`
	// AcceptanceChecks are the runnable criteria of the work items this run was promoted from, with
	// the DIRECTION each must move. An "acceptance" check must FAIL before the work and PASS after —
	// that is what distinguishes done from not-done. A "guard" must pass at both ends.
	//
	// The distinction is load-bearing: a task whose only check passes before a line is written cannot
	// prove anything, and the failure is silent — it reaches a reviewer looking finished.
	AcceptanceChecks []RunAcceptanceCheck `json:"acceptance_checks,omitempty"`
	// ReviewOf (review agent) is set on a hidden preset:"review" run — the id of the TARGET run this
	// job reviews. The daemon fetches the target's per-task branches via ReviewTarget, diffs each, and
	// records a graded review via RecordReview. Reference-not-command: it's an id, never a path.
	ReviewOf string `json:"review_of,omitempty"`
	// RebaseOf (Slice A2) is set on a hidden preset:"rebase" run — the id of the TARGET run whose PR
	// branch this job rebases onto the base. DATA only, same posture as ReviewOf.
	RebaseOf string `json:"rebase_of,omitempty"`
	// Model (model selection) is the build model for THIS run — the daemon forwards it to crank's
	// --model, which passes it to the worker's engine (--model). Empty = the engine/host default.
	// Reference-not-command: a plain model token (validated against modelRe before it reaches the argv),
	// never a path or a flag. The engine (claude/codex/…) is the project's own per-project choice.
	Model string `json:"model,omitempty"`
	// ToolGrants (#575) are the org's BUILD-role tool grants for this project — MCP server NAMES
	// and shell command PREFIXES only (DATA). The daemon hands them to crank as a file
	// (--grants-file, parallel to --globals-file); the worker re-validates the shapes and resolves
	// MCP names against its OWN local catalog (resolveLaunchGrants) exactly like planning launches.
	// Absent = the unchanged allowlist posture. Never attached to review/rebase/describe jobs.
	ToolGrants *ToolGrants `json:"tool_grants,omitempty"`
	// Engine (Epic #73) is the server-resolved engine for this run (run override > the project's
	// phase engine — build_engine for spec/build presets, review_engine for review), stamped on
	// runs.engine and carried on every type:"run" envelope (key absent when unset). Reference-
	// not-command: the daemon validates it against the LOCAL engines registry (resolveRunEngine)
	// before it can influence an argv; empty/absent/unknown = the local registry's per-project
	// engine ("" = claude), the pre-field behavior.
	Engine string `json:"engine,omitempty"`
	// VisualVerify (T2d) turns on the visual verify gate for this run's project FROM THE WEB — no
	// repo `.partyline/visual` file needed. It is a pure boolean TOGGLE (safe control-plane data),
	// never a script: the daemon passes it to crank as --visual. The render HOW stays either the
	// repo-trusted `.partyline/visual` script or a daemon-hardcoded framework preset — the web never
	// ships executable render code to a daemon (same RCE line as web-triggered updates).
	VisualVerify bool `json:"visual_verify"`
	// VisualRoutes are SAFE render DATA for the daemon's framework preset: app paths to screenshot
	// (e.g. "/dashboard"). Data only — the daemon validates each against a strict path shape and
	// injects them into its OWN hardcoded render script; a route can never become a command.
	VisualRoutes []string `json:"visual_routes,omitempty"`
	// Checks and ReviewLanes are the project's PIPELINE POLICY (G.6): which of the repo's named
	// acceptance checks run and whether they block, and which reviewer lanes judge the diff.
	//
	// POLICY, NEVER A COMMAND — the same line VisualRoutes draws. The repo's `.partyline/verify` and
	// `.partyline/review` remain the sole source of what actually executes; these only say which of
	// those run and how hard. There is deliberately no command field, here or in the tables behind
	// it, because a server-supplied command executed by a daemon is RCE on every machine in the
	// fleet. The daemon re-validates both and writes them to a file crank reads — never argv.
	//
	// Absent = the default pipeline: every repo check blocking and always-run, one reviewer.
	Checks      []RunCheckPolicy `json:"checks,omitempty"`
	ReviewLanes []RunReviewLane  `json:"review_lanes,omitempty"`
	// GitProvider is the org's active code-repository provider (github|gitlab|bitbucket). Only GitHub
	// has a brokered PR integration; for gitlab/bitbucket the daemon pushes the branch over SSH and the
	// human opens the MR/PR, so the merge step skips `gh pr create` and emits a provider-correct note
	// (no "connect GitHub" misdirection). Absent/empty = github (the pre-field behavior; old daemons
	// ignore it). Reference-not-command: a plain enum, never a path or flag.
	GitProvider string `json:"git_provider,omitempty"`
	// ChainBranch is the branch EVERY task of this run must build on, set when the run belongs to a
	// chain. A chain is one deliverable assembled in series: member 1 creates the branch, members 2..N
	// receive its name here and continue it, so each step opens the files the previous step just edited
	// and the whole chain reviews as ONE PR. Empty = unchained (crank derives a branch per task).
	// Reference-not-command: a plain branch name, shape-validated by the daemon before it can reach an
	// argv and re-slugged by gitwt before it becomes a ref.
	ChainBranch string `json:"chain_branch,omitempty"`
	// BaseBranch is the project's configured base branch: the ref this run's work FORKS FROM and the
	// ref its PR TARGETS. One value drives both on purpose — forking from `main` while targeting
	// `staging` produces a PR containing every commit that differs between them, so they can never be
	// set independently. Empty = the repo's own default branch (origin/HEAD), the pre-setting behavior
	// that old daemons fall back to by simply not knowing this key. Reference-not-command: a plain
	// branch name, shape-validated here before it can reach an argv.
	BaseBranch string `json:"base_branch,omitempty"`
	// Provisioned (docs/plans/provisioned-workers.md, P2) marks a clone-on-demand run: the label is NOT
	// in this daemon's local registry, so instead of resolving it there the daemon fetches the repo
	// manifest (RunProvisionManifest), clones the repo into a managed dir, and runs there. Only honored
	// when the operator opted the node in (`ptln daemon provision on`). Absent/false = the normal
	// registry path (old daemons ignore the key and would fail label resolution — which is why the web
	// hard-gates provisioned dispatch on a minimum version).
	Provisioned bool `json:"provisioned,omitempty"`
}

// ProvisionManifest is the clone manifest for a provisioned run (GET /daemon/run/[id]/provision):
// inert DATA the daemon resolves against its own trust domain. The daemon derives the clone PATH
// itself from Repo.FullName (re-validated) and fetches the clone CREDENTIAL separately (github-token)
// — the manifest carries neither a path nor a secret.
type ProvisionManifest struct {
	Repo struct {
		FullName      string `json:"full_name"`
		RepoID        int64  `json:"repo_id"`
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"clone_url"`
	} `json:"repo"`
	Label string `json:"label"`
}

// RunProvisionManifest fetches the clone manifest for a provisioned run (device-token; the server's
// 404-wall requires the run to be THIS daemon's and actually provisioned).
func RunProvisionManifest(base, token, runID string) (*ProvisionManifest, error) {
	var m ProvisionManifest
	if err := daemonDo("GET", base, token, "/api/v1/daemon/run/"+runID+"/provision", nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DaemonStream holds the daemon's outbound, long-lived SSE connection to the control plane
// using its DEVICE token (not the login token). onReady fires at `: ready`; onLaunch fires
// per PENDING request (a notification — no join ref — that the owner may approve); onAccepted
// fires when a request has become `accepted` (by ANY surface — the web modal or a CLI) and
// carries the single-use join ref, so it's the EXECUTION trigger (exchange+spawn); onRun fires
// per QUEUED run-profile (O.2) so the daemon can reconcile it and drive crank. onKill fires
// with a request id when a party member asks to stop that launch; onKillRun / onPauseRun /
// onResumeRun fire with a RUN id to SIGTERM / SIGSTOP / SIGCONT that run's crank process group
// (crank-01 pause/resume mirror the kill signal path). Returns when the connection drops so the
// caller reconnects.
// ConsultEvent (ask_peer P0.a): a teammate asking THIS daemon for read-only feedback on a plan,
// scoped to an advertised label. A REFERENCE (label) + the question TEXT — never a path or command.
// The daemon resolves the label against its own local registry and answers read-only (P0.0).
type ConsultEvent struct {
	Type         string `json:"type"`
	ConsultID    string `json:"consult_id"`
	ProjectLabel string `json:"project_label"`
	Question     string `json:"question"`
	// The PROJECT's daily auto-answer allowance (projects.consult_auto_daily), resolved server-side
	// from project settings — one number so a web edit reaches every machine in the project. A POINTER
	// because 0 is a real setting ("never auto-answer") and absent means "no setting: use the built-in
	// default". SPEND DATA, not a privilege: it can never turn a queued consult into an auto-answered
	// one (policy runs first), and the daemon clamps it to its own ceiling (consult_budget.go).
	ConsultAutoDaily *int `json:"consult_auto_daily,omitempty"`
	// WHO is asking, resolved server-side from profiles — never taken from the request, since letting
	// the asker name themselves is the abuse vector consult_budget.go exists to close. Empty when
	// unknown, and the prompt says so rather than inventing a name. Before this the approval prompt
	// showed a question with NO attribution, which is a poor thing to put a human decision behind.
	FromHandle string `json:"from_handle,omitempty"`
	// What should ANSWER: the project's consult engine + model (projects.consult_engine /
	// consult_model, model falling back to review_model server-side).
	//
	// Quality, not cost. A consult answer is the only agent output in the product with no gate behind
	// it — a crank build's weak model is caught by the verify gate, while this paragraph goes straight
	// into the asking agent's context and steers its work. Until now no model was passed at all, so
	// answers ran on whatever that box's CLI happened to default to.
	//
	// Both are SUGGESTIONS, same posture as ConsultAutoDaily: the engine goes through preferEngine()
	// and falls back to the machine's local per-project engine when it names one this box cannot run,
	// because which CLI is installed and logged in is a fact only the answering machine has.
	ConsultEngine string `json:"consult_engine,omitempty"`
	ConsultModel  string `json:"consult_model,omitempty"`
}

// ConsultAnswerEvent (ask_peer P0.a): a peer's reply to a consult THIS daemon asked, routed back so
// it can surface in the asking session (P0.d). The answer is DATA (untrusted feedback), never a command.
type ConsultAnswerEvent struct {
	Type         string `json:"type"`
	ConsultID    string `json:"consult_id"`
	ProjectLabel string `json:"project_label"`
	Answer       string `json:"answer"`
}

// ConsultCancelEvent (ask_peer): the ASKER withdrew a question addressed to this daemon. The daemon
// drops it from its pending set — without this it keeps holding a question nobody is waiting for, and
// offers its owner an approve that can only fail. An id and a label; nothing to execute.
type ConsultCancelEvent struct {
	Type         string `json:"type"`
	ConsultID    string `json:"consult_id"`
	ProjectLabel string `json:"project_label"`
}

func DaemonStream(ctx context.Context, base, token string, onReady func(), onLaunch func(LaunchEvent), onAccepted func(LaunchEvent), onRun func(RunEvent), onKill func(string), onKillRun func(string), onPauseRun func(string), onResumeRun func(string), onRestart func(), onRelabel func(RelabelEvent), onBindRepo func(BindRepoEvent), onUpdate func(), onConsult func(ConsultEvent), onConsultAnswer func(ConsultAnswerEvent), onConsultCancel func(ConsultCancelEvent)) error {
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
	// Dead-stream watchdog. The server emits a `: ping` comment every ~20s, so a healthy
	// connection is never silent for long — but a HALF-OPEN TCP connection (laptop sleep/wake,
	// NAT reset, network change) blocks sc.Scan() forever with no error, and the daemon's
	// separate heartbeat loop keeps last_seen fresh the whole time: the machine shows "online"
	// while hearing nothing, and accepted work parks indefinitely. If the stream goes quiet past
	// streamStallTimeout, close the body so the scanner unblocks and the caller reconnects — a
	// fresh connection re-pushes everything still pending server-side, so recovery is complete.
	var stalled atomic.Bool
	watchdog := time.AfterFunc(streamStallTimeout, func() { stalled.Store(true); res.Body.Close() })
	defer watchdog.Stop()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		watchdog.Reset(streamStallTimeout)
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
			if probe.Type == "kill_run" { // stop a RUN — SIGTERM the crank process group we spawned
				var kr struct {
					RunID string `json:"run_id"`
				}
				if json.Unmarshal([]byte(raw), &kr) == nil && kr.RunID != "" && onKillRun != nil {
					onKillRun(kr.RunID)
				}
				continue
			}
			if probe.Type == "pause_run" { // hold a RUN — SIGSTOP the crank process group we spawned
				var pr struct {
					RunID string `json:"run_id"`
				}
				if json.Unmarshal([]byte(raw), &pr) == nil && pr.RunID != "" && onPauseRun != nil {
					onPauseRun(pr.RunID)
				}
				continue
			}
			if probe.Type == "resume_run" { // release a held RUN — SIGCONT the crank process group
				var rr struct {
					RunID string `json:"run_id"`
				}
				if json.Unmarshal([]byte(raw), &rr) == nil && rr.RunID != "" && onResumeRun != nil {
					onResumeRun(rr.RunID)
				}
				continue
			}
			if probe.Type == "restart" { // owner-triggered restart — daemon-level, no request_id
				if onRestart != nil {
					onRestart()
				}
				continue
			}
			if probe.Type == "update" { // web-requested self-update — a NUDGE, never a payload: the
				// daemon runs its OWN guarded upgrade (same public release + installer every install
				// uses; service-managed, idle, newer-only). No fields to decode.
				if onUpdate != nil {
					onUpdate()
				}
				continue
			}
			if probe.Type == "relabel_project" { // project rename cascade — two LABEL strings, never a path
				var re RelabelEvent
				if json.Unmarshal([]byte(raw), &re) == nil && re.OldLabel != "" && re.NewLabel != "" && onRelabel != nil {
					onRelabel(re)
				}
				continue
			}
			if probe.Type == "bind_repo" { // Repository access → Local directory: bind a repo THIS machine advertised
				var be BindRepoEvent
				// Either half is enough: a plain bind carries handle, a clone assignment carries
				// destination_handle instead (the two namespaces never mix — see BindRepoEvent).
				if json.Unmarshal([]byte(raw), &be) == nil && (be.Handle != "" || be.DestinationHandle != "") && be.Label != "" && onBindRepo != nil {
					onBindRepo(be)
				}
				continue
			}
			if probe.Type == "consult" { // ask_peer: a teammate asks for read-only feedback — label + question, never a command
				var ce ConsultEvent
				if json.Unmarshal([]byte(raw), &ce) == nil && ce.ConsultID != "" && onConsult != nil {
					onConsult(ce)
				}
				continue
			}
			if probe.Type == "consult_cancel" { // ask_peer: the asker withdrew a question we were holding — drop it
				var cc ConsultCancelEvent
				if json.Unmarshal([]byte(raw), &cc) == nil && cc.ConsultID != "" && onConsultCancel != nil {
					onConsultCancel(cc)
				}
				continue
			}
			if probe.Type == "consult_answer" { // ask_peer: a peer's reply to a consult we asked — DATA, routed back to the asking session
				var ca ConsultAnswerEvent
				if json.Unmarshal([]byte(raw), &ca) == nil && ca.ConsultID != "" && onConsultAnswer != nil {
					onConsultAnswer(ca)
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
	if stalled.Load() {
		return fmt.Errorf("stream silent for %s (dead connection?) — reconnecting", streamStallTimeout)
	}
	return sc.Err()
}

// streamStallTimeout is how long the daemon tolerates a silent stream before declaring the
// connection dead. The server pings every ~20s, so 90s is 4+ missed pings — a real outage,
// not jitter.
const streamStallTimeout = 90 * time.Second

// PostConsultAnswer (ask_peer P0.a): the daemon posts its read-only answer for a consult addressed to
// it. Device-token authed; the server scopes the write to this daemon (a token can only answer its
// own consults). Mirrors RecordReview — a one-shot POST back up the same authenticated channel.
func PostConsultAnswer(base, token, consultID, answer string) error {
	return daemonDo("POST", base, token, "/api/v1/daemon/consult/"+consultID+"/answer",
		map[string]string{"answer": answer}, nil)
}

// DeclineConsult (ask_peer P0.c): the daemon declines a consult — the owner denied it, or the project
// isn't answerable on this machine. Same route + scoping as PostConsultAnswer; frees the asker at once
// instead of leaving them to the server-side timeout.
func DeclineConsult(base, token, consultID, reason string) error {
	return daemonDo("POST", base, token, "/api/v1/daemon/consult/"+consultID+"/answer",
		map[string]any{"decline": true, "detail": reason}, nil)
}

// RunStatus reads the server's CURRENT status for one of this daemon's runs. The startup orphan
// sweep uses it so a dead local pid only parks a run the server still believes is in flight —
// never one that finished and reported before the machine went down.
func RunStatus(base, token, runID string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	if err := daemonDo("GET", base, token, "/api/v1/daemon/run/"+runID+"/status", nil, &out); err != nil {
		return "", err
	}
	return out.Status, nil
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

// DaemonProjectRef is a mirrored launch target — LABEL + preset + engine only, NEVER a path.
type DaemonProjectRef struct {
	Label  string `json:"label"`
	Preset string `json:"preset"`
	// Engine is this machine's registered per-project engine ("" = claude, the default —
	// omitted on the wire). A hint for the web's pickers/phase-engine resolution; the local
	// registry stays the authority at execution time (resolveLaunch / resolveRunEngine).
	Engine string `json:"engine,omitempty"`
}

// MirrorProjects (R2) replaces this daemon's advertised labels server-side. Paths cannot
// leak: the payload has no path field by construction.
func MirrorProjects(base, token string, projects []DaemonProjectRef) error {
	return daemonDo("PUT", base, token, "/api/v1/daemon/projects", map[string]any{"projects": projects}, nil)
}

// DaemonConfig is the METADATA-ONLY snapshot a heartbeat carries (Fleet map #267). It exists so
// the web can show what a machine is + what it can run. SECURITY INVARIANT: never secrets — no
// absolute paths, no file contents, no MCP URLs, no tokens. Only the fields below, by construction.
type DaemonConfig struct {
	Version    string              `json:"version"`
	OS         string              `json:"os"`
	Projects   []DaemonProjectInfo `json:"projects"`
	Provision  bool                `json:"provision,omitempty"`   // P2: node opted into clone-on-demand work (ptln daemon provision on)
	AutoUpdate bool                `json:"auto_update,omitempty"` // node self-updates while idle (ptln daemon autoupdate on) — lets the fleet map show which nodes will drift
	// EngineAccount is the ANTHROPIC identity the machine's `claude` CLI is logged in as ("email ·
	// org") — the account crank actually SPENDS when it builds. Surfaced because it was invisible: a
	// run can silently bill a different account/org than you think (a switched login, a team vs
	// personal org), and the only symptom was a confusing quota block. Best-effort, identity only —
	// never a token. Empty when unreadable or the engine isn't claude.
	EngineAccount string `json:"engine_account,omitempty"`
	// MCPCatalog is the NAMES of the MCP servers in this machine's local catalog (~/.partyline/
	// mcp.json) — metadata only, never commands/env/keys (#267 posture: secrets = metadata, not
	// values). It exists so the web "Agent tools" picker (#574) can offer real, machine-resident
	// tools for per-project grants; the daemon later resolves a granted NAME back to its own local
	// definition (reference-not-command — the server can only ever name what a machine advertised).
	MCPCatalog []string `json:"mcp_catalog,omitempty"`
	// LocalRepos are the git repositories on this machine that the web may offer as a project
	// ("Local directory" in Repository access) — handles + display names, never paths, same
	// posture as everything else here.
	LocalRepos []LocalRepo `json:"local_repos,omitempty"`
	// Destinations are the directories this machine is willing to CLONE INTO — the second half of
	// web-driven project provisioning. A SEPARATE namespace from LocalRepos on purpose: merging the
	// two would let the web ask a machine to clone over an existing working tree, so `handle` is
	// only ever matched against LocalRepos and `destination_handle` only ever against these.
	// Same posture as everything else here: an opaque handle plus display metadata, never a path.
	Destinations []Destination `json:"destinations,omitempty"`
	// Metrics is this machine's health AS OF THIS BEAT — the fleet card's "can this node take
	// work" answer. Nil when nothing could be measured, so the key vanishes rather than shipping
	// an object full of zeroes.
	Metrics *NodeMetrics `json:"metrics,omitempty"`
}

// NodeMetrics is the bounded health snapshot the heartbeat carries (fleet node health). Same
// posture as everything else in DaemonConfig: numbers about the machine, never anything about its
// contents. The server independently clamps every field into a declared range and DROPS anything
// that is not a finite number, so these are a best effort the control plane never has to trust.
//
// ABSENT ≠ ZERO, and that distinction is the whole reason for the pointers below. "This node never
// reported memory" and "this node has no memory" must not render the same, so a field we could not
// measure has to be an ABSENT KEY. That splits the fields in two:
//
//   - Pointers where 0 is a REAL reading: an idle machine genuinely is at 0% CPU, load 0.00, and a
//     fresh volume at 0% used. `omitempty` on a plain float64 would silently delete exactly those.
//   - Plain values where 0 is NOT a reading: cpu_cores (the server's floor is 1) and mem_total_mb
//     (a machine with no RAM does not exist), so `omitempty` alone says "unknown" correctly.
type NodeMetrics struct {
	CPUPct      *float64 `json:"cpu_pct,omitempty"`   // 0–100, utilisation over the interval since the previous beat
	Load1       *float64 `json:"load1,omitempty"`     // 1-minute load average
	CPUCores    int      `json:"cpu_cores,omitempty"` // load1 is meaningless without this: load 8 is a wedged laptop and a bored 32-core box
	MemUsedPct  *float64 `json:"mem_used_pct,omitempty"`
	MemTotalMB  int64    `json:"mem_total_mb,omitempty"`
	DiskUsedPct *float64 `json:"disk_used_pct,omitempty"`
	DiskFreeGB  *float64 `json:"disk_free_gb,omitempty"`
	// DiskMount names WHICH volume the disk numbers describe — they are the TIGHTEST volume the
	// machine found, not the root volume, because a box whose /data is full cannot build while its
	// / looks roomy. Without this the card would be showing a percentage of something unnamed.
	DiskMount string `json:"disk_mount,omitempty"`
	UptimeS   *int64 `json:"uptime_s,omitempty"`
}

// Empty reports whether nothing at all was measured, so the caller can omit the block entirely
// instead of sending `"metrics":{}`.
func (m *NodeMetrics) Empty() bool {
	if m == nil {
		return true
	}
	return m.CPUPct == nil && m.Load1 == nil && m.CPUCores == 0 && m.MemUsedPct == nil &&
		m.MemTotalMB == 0 && m.DiskUsedPct == nil && m.DiskFreeGB == nil && m.DiskMount == "" &&
		m.UptimeS == nil
}

// DaemonProjectInfo is one advertised project as metadata: the label/preset/engine plus the dir
// BASENAME only (never the absolute path — that stays on the machine).
type DaemonProjectInfo struct {
	Label   string `json:"label"`
	Preset  string `json:"preset,omitempty"`
	Engine  string `json:"engine,omitempty"`
	DirBase string `json:"dir_base,omitempty"`
	// Checks are the NAMES of the acceptance checks this project's repo declares in
	// .partyline/verify (G.4/G.6). Names only — never the commands.
	//
	// Same posture as MCPCatalog above, for the same reason: the settings page needs to offer the
	// checks that actually exist so an operator picks rather than types, and a typo'd policy row is
	// silently ignored. Sending the COMMANDS would put repo-authored shell in the control plane's
	// hands, which is the boundary the whole design refuses; sending names lets the server describe
	// what the machine already told it about, and the machine resolves a name back to its own
	// command at run time.
	//
	// Only NAMED checks appear. An auto-named check's name moves when its command changes, so
	// offering it as a policy target would bind a settings toggle to something that shifts
	// underneath it.
	Checks []string `json:"checks,omitempty"`
}

// LocalRepo is one git repository the machine is willing to offer as a project ("Local directory"
// in Repository access). Handle is an opaque hash of the local absolute path; Name and Parent are
// DISPLAY ONLY, so a human can tell ~/dev/app from ~/work/app in the picker. The absolute path is
// never carried — the daemon maps a handle back to a path by re-deriving its own list.
type LocalRepo struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"`
}

// Destination is one PARENT directory the machine could put a new checkout in ("clone it onto that
// box"). Handle is an opaque hash of the local absolute path, exactly like LocalRepo — the path
// itself never leaves the machine, and an assignment can only ever name a directory this machine
// already offered. Parent and Label are DISPLAY ONLY: Parent renders where it is ("~/dev"), Label
// says what it is ("default workspace", "scan root") so the picker is decidable by a human.
type Destination struct {
	Handle string `json:"handle"`
	Parent string `json:"parent,omitempty"`
	Label  string `json:"label,omitempty"`
}

// AssignmentState reports one transition of a project assignment (queued→cloning→registering→ready,
// or failed) to the control plane. Only the daemon the assignment was addressed to may write it;
// reporting is at-least-once, so a repeat of the current state is a no-op server-side. Reason is a
// plain-language sentence shown to the human — it is the ONLY explanation they get for a refusal,
// so it names the directory or the remote that failed.
func AssignmentState(base, token, assignmentID, state, reason string) error {
	id := strings.TrimSpace(assignmentID)
	if id == "" {
		return fmt.Errorf("no assignment id")
	}
	body := map[string]any{"state": state}
	if reason != "" {
		body["reason"] = reason
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/assignments/"+url.PathEscape(id)+"/state", body, nil)
}

// RelabelEvent (projects rename cascade) asks the daemon to rename a project in its OWN local
// registry old→new and re-mirror — the only way a machine-advertised label actually changes (the
// heartbeat mirror would otherwise revert a server-side edit). Two labels, never a path.
type RelabelEvent struct {
	Type     string `json:"type"`
	OldLabel string `json:"old_label"`
	NewLabel string `json:"new_label"`
}

// BindRepoEvent asks the daemon to register one of the repositories IT ADVERTISED as a project
// under the given label. Handle is the daemon's own opaque id (see LocalRepo) — the server cannot
// name a directory the machine never offered, and nothing here becomes a path or an argv.
//
// It carries EITHER Handle (bind a checkout the machine already has) or DestinationHandle + RepoURL
// (clone the repo into a directory the machine offered as a destination). The two handle fields are
// disjoint namespaces and are never cross-matched. AssignmentID, when present, is what the daemon
// reports state transitions against — it is a server-side id, never a path.
// RunAcceptanceCheck is one runnable criterion. Command is DATA the worker executes in its own
// worktree — never interpolated into another command, and never a path.
type RunAcceptanceCheck struct {
	Command   string `json:"command"`
	Direction string `json:"direction"` // "acceptance" (red→green) | "guard" (green→green)
	Text      string `json:"text"`      // the human-readable criterion, for the report
}

type BindRepoEvent struct {
	Type   string `json:"type"`
	Handle string `json:"handle"`
	Label  string `json:"label"`
	Preset string `json:"preset,omitempty"`
	Engine string `json:"engine,omitempty"`
	// Policy is the project's RUN MODE on this machine: "auto" runs dispatched work unattended,
	// "ask" queues it for the owner to approve at the daemon console. Empty means "leave whatever
	// this machine already has" — an assignment that only meant to bind a directory must never
	// silently widen (or narrow) the standing grant to run code on someone's box.
	Policy            string `json:"policy,omitempty"`
	AssignmentID      string `json:"assignment_id,omitempty"`
	DestinationHandle string `json:"destination_handle,omitempty"`
	RepoURL           string `json:"repo_url,omitempty"`
}

// Heartbeat (#267) refreshes this daemon's liveness (server touches last_seen) and mirrors the
// config-metadata snapshot, so a long-lived stream doesn't read as stale and the fleet map shows
// what the machine is. Best-effort telemetry: a failure never affects the daemon.
func Heartbeat(base, token string, cfg DaemonConfig) error {
	return daemonDo("POST", base, token, "/api/v1/daemon/heartbeat", map[string]any{"config": cfg}, nil)
}

// Environment delta (epic #683) — the two-way seam between the control plane's INTENT (which
// environments a project has, in what order) and the machine's TRUTH (what git says).
//
// Everything travelling DOWN is a reference: environment names, branch names, run ids. Nothing is a
// path and nothing is a command — the daemon re-validates every branch name against its own regex
// before it reaches git, so the worst a hostile control plane could send is a string that gets
// rejected. Everything travelling UP is metadata about commits (short shas, subjects, author names)
// and never a diff or file contents.

type EnvStep struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
}

// EnvBranchRef is one of partyline's OWN task branches — the thing that lets a commit in the gap be
// mapped back to the run and work item that produced it, with no guessing.
type EnvBranchRef struct {
	Branch string `json:"branch"`
	RunID  string `json:"run_id"`
}

// EnvQuestion is what the control plane asks a machine to measure for one project.
type EnvQuestion struct {
	Label        string         `json:"label"`
	Environments []EnvStep      `json:"environments"`
	Branches     []EnvBranchRef `json:"branches"`
}

type EnvCommit struct {
	Sha     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	At      string `json:"at"`
}

type EnvItem struct {
	Branch string `json:"branch"`
	RunID  string `json:"run_id"`
}

// EnvGap is what is in one environment that has not reached the next.
type EnvGap struct {
	Position    int         `json:"position"`
	FromName    string      `json:"from_name"`
	ToName      string      `json:"to_name"`
	FromBranch  string      `json:"from_branch"`
	ToBranch    string      `json:"to_branch"`
	CommitCount int         `json:"commit_count"`
	Authors     []string    `json:"authors"`
	Commits     []EnvCommit `json:"commits"`
	Items       []EnvItem   `json:"items"`
}

type EnvReport struct {
	Label string   `json:"label"`
	Gaps  []EnvGap `json:"gaps"`
}

// EnvQuestions asks what this machine should measure. Only projects it already advertises, only
// those with a chain of two or more environments.
func EnvQuestions(base, token string) ([]EnvQuestion, error) {
	var out struct {
		Projects []EnvQuestion `json:"projects"`
	}
	if err := daemonDo("GET", base, token, "/api/v1/daemon/environments", nil, &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

// ReportEnvDeltas sends the measured gaps back. Best-effort telemetry about the machine's own
// repos: a failure just means the web keeps showing the previous (older) numbers.
func ReportEnvDeltas(base, token string, reports []EnvReport) error {
	return daemonDo("POST", base, token, "/api/v1/daemon/environments", map[string]any{"projects": reports}, nil)
}

// SendTelemetry posts the anonymous "active install" ping (Telemetry C): a random client
// install-id + version + os, nothing else. No auth (it's identity-free by design). Best-effort — a
// failure is ignored by the caller; the CLI gates this on the user's telemetry opt-out.
func (c *Client) SendTelemetry(installID, cliVersion, goos string) error {
	body, _ := json.Marshal(map[string]string{"install_id": installID, "version": cliVersion, "os": goos})
	req, err := http.NewRequest("POST", c.Base+"/api/v1/telemetry", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
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
	return SetRunStatusReason(base, token, runID, status, "", detail)
}

// SetRunStatusReason is SetRunStatus carrying a pause reason (Epic G.2), for the transitions that
// pause a run rather than end it. Only meaningful with status=needs_approval; other statuses pass
// an empty reason and the server clears any stale one.
func SetRunStatusReason(base, token, runID, status, reason, detail string) error {
	in := map[string]any{"status": status}
	if detail != "" {
		in["detail"] = detail
	}
	if reason != "" {
		in["pause_reason"] = reason
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID, in, nil)
}

// ReviewTargetTask is one task of the run being reviewed: its worklist string + the branch crank
// prepared for it. The daemon diffs each branch against its base to feed the reviewer.
type ReviewTargetTask struct {
	Idx    int    `json:"idx"`
	Task   string `json:"task"`
	Branch string `json:"branch"`
}

// ReviewTarget (review agent) fetches what the hidden review run must review: the TARGET run's id +
// its per-task branches. Device-token (the daemon owns the review run it's executing). reviewRunID is
// the review run's own id (ev.RunID); the server resolves its review_of.
// ReviewCriterion is one acceptance criterion from the plan item the target run was promoted from —
// the explicit checklist the reviewer grades against (Verify optionally says HOW to check it).
type ReviewCriterion struct {
	Text   string `json:"text"`
	Verify string `json:"verify"`
}

func ReviewTarget(base, token, reviewRunID string) (string, []ReviewTargetTask, []ReviewCriterion, []string, error) {
	var r struct {
		TargetRunID string             `json:"target_run_id"`
		Tasks       []ReviewTargetTask `json:"tasks"`
		Criteria    []ReviewCriterion  `json:"acceptance_criteria"`
		Canon       []string           `json:"canon"` // project constraints/contracts the diff must not violate (check-criteria)
	}
	if err := daemonDo("GET", base, token, "/api/v1/daemon/run/"+reviewRunID+"/review-target", nil, &r); err != nil {
		return "", nil, nil, nil, err
	}
	return r.TargetRunID, r.Tasks, r.Criteria, r.Canon, nil
}

// ReviewIssue is one finding in a graded review: a severity (high|med|low) + the finding text.
type ReviewIssue struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
}

// RecordReview (review agent) posts the graded verdict for the TARGET run — it lands in run_reviews
// (the board card + run detail render it). Device-token; the server org-scopes the write. Advisory:
// this never changes the target run's status.
func RecordReview(base, token, targetRunID, grade, summary, reviewerModel string, issues []ReviewIssue, freshTokens, cacheReadTokens int, costUSD float64) error {
	in := map[string]any{"grade": grade}
	if summary != "" {
		in["summary"] = summary
	}
	if reviewerModel != "" {
		in["reviewer_model"] = reviewerModel
	}
	if len(issues) > 0 {
		in["issues"] = issues
	}
	// Token/cost accounting for the review one-shot — so the run detail can show build + review as
	// one bill. Only send what the engine actually reported (claude fills these; others send 0), so a
	// no-usage engine writes nulls rather than a fake 0. Cost is claude's own total_cost_usd.
	if freshTokens > 0 {
		in["fresh_tokens"] = freshTokens
	}
	if cacheReadTokens > 0 {
		in["cache_read_tokens"] = cacheReadTokens
	}
	if costUSD > 0 {
		in["cost_usd"] = costUSD
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+targetRunID+"/review-result", in, nil)
}

// RunGitHubToken (crank merge step) fetches a SHORT-LIVED GitHub App installation token for opening
// this run's PR, so the daemon needs no durable local credential. Device-token; the server derives
// the org and scope from the run (reference-not-command) and returns the token once. Any error —
// including 404, which is how the endpoint signals "this org hasn't connected GitHub" — leaves the
// caller to fall back to the operator's local token. The token is used immediately, never persisted.
func RunGitHubToken(base, token, runID string) (string, error) {
	var r struct {
		Token string `json:"token"`
	}
	if err := daemonDo("GET", base, token, "/api/v1/daemon/run/"+runID+"/github-token", nil, &r); err != nil {
		return "", err
	}
	return r.Token, nil
}

// EnqueueRun creates a QUEUED run (a Backlog card) via the daemon/run enqueue seam — the SAME
// endpoint the web "describe a task" flow uses (POST /api/v1/daemon/run). USER-authed (Bearer,
// via c.do), so `ptln describe` enqueues as the person, not a device token. Reference-not-command:
// tasks are DATA the target daemon later resolves against its OWN registry — never argv. Returns the
// new run id. Server enforces the trust wall (thread-org membership, daemon advertises the label).
func (c *Client) EnqueueRun(daemonID, threadID, projectLabel, preset, mergePolicy string, tasks []string) (string, error) {
	in := map[string]any{
		"daemon_id":     daemonID,
		"thread_id":     threadID,
		"project_label": projectLabel,
		"preset":        preset,
		"merge_policy":  mergePolicy,
		"tasks":         tasks,
	}
	var r struct {
		RunID string `json:"run_id"`
	}
	if err := c.do("POST", "/api/v1/daemon/run", in, &r); err != nil {
		return "", err
	}
	return r.RunID, nil
}

// WorkItemCriterion is one acceptance criterion on a work item: a verifiable statement + how it's
// checked (executable check | adversarial review | behavior review).
type WorkItemCriterion struct {
	Text   string `json:"text"`
	Verify string `json:"verify"`
}

// CreateWorkItem creates one node in the work-items planning tree (Epic ▸ Feature ▸ Task) via the
// user-authed API (POST /api/v1/work-items). Backs the `/describe` MCP flow: the user's own agent
// interviews, then records a scored node into the thread's backlog. kind is epic|feature|task;
// parentID nests it (server enforces the depth cap); empty fields are omitted. Returns the new id.
func (c *Client) CreateWorkItem(threadID, kind, title, parentID, document string, readiness int, criteria []WorkItemCriterion) (string, error) {
	in := map[string]any{"thread_id": threadID, "kind": kind, "title": title}
	if parentID != "" {
		in["parent_id"] = parentID
	}
	if document != "" {
		in["document"] = document
	}
	if readiness > 0 {
		in["readiness"] = readiness
	}
	if len(criteria) > 0 {
		in["acceptance_criteria"] = criteria
	}
	var r struct {
		ID string `json:"id"`
	}
	if err := c.do("POST", "/api/v1/work-items", in, &r); err != nil {
		return "", err
	}
	return r.ID, nil
}

// ImportTicket starts a PLANNING SESSION from a ticket in the team's own tracker, seeded with the
// ticket verbatim. It does not create a task: a raw ticket is a problem statement, and the describe
// conversation is what turns it into work that can be built.
//
// Idempotent on (source_tool, source_id) — a second import of the same ticket returns the existing
// session rather than starting a rival one. Returns the session URL and whether it was newly created.
func (c *Client) ImportTicket(threadID, title, document, sourceTool, sourceID, sourceURL string) (string, bool, error) {
	in := map[string]any{
		"thread_id": threadID, "title": title,
		"source_tool": sourceTool, "source_id": sourceID,
	}
	if document != "" {
		in["document"] = document
	}
	if sourceURL != "" {
		in["source_url"] = sourceURL
	}
	var r struct {
		URL     string `json:"url"`
		Created bool   `json:"created"`
	}
	if err := c.do("POST", "/api/v1/work-items/import", in, &r); err != nil {
		return "", false, err
	}
	return r.URL, r.Created, nil
}

// WorkTreeNode is one node of a DECOMPOSITION — the nested epic▸feature▸task tree describe produces
// (Part 1). Children carry the same shape recursively; kinds are decided by the decomposer.
type WorkTreeNode struct {
	Kind               string              `json:"kind"`
	Title              string              `json:"title"`
	Document           string              `json:"document,omitempty"`
	AcceptanceCriteria []WorkItemCriterion `json:"acceptance_criteria,omitempty"`
	Readiness          int                 `json:"readiness,omitempty"`
	Children           []WorkTreeNode      `json:"children,omitempty"`
}

// CreateWorkTree records a whole decomposition in one call (POST /api/v1/work-items/tree). The server
// validates the depth cap AND that each task leaf fits the run cap, then threads the inserts. Returns
// the root id + total node count. On a validation failure the server's (actionable) error is returned.
func (c *Client) CreateWorkTree(threadID string, root WorkTreeNode) (rootID string, count int, err error) {
	return c.CreateWorkTreeFrom(threadID, root, "")
}

// CreateWorkTreeFrom is CreateWorkTree with the PARTY the plan came from, recorded on the root node
// (origin_party_id). That back-link is what makes a filed tree reachable from the conversation that
// produced it — the chain drawer and the item's own header both follow it. Empty originPartyID
// behaves exactly like CreateWorkTree.
func (c *Client) CreateWorkTreeFrom(threadID string, root WorkTreeNode, originPartyID string) (rootID string, count int, err error) {
	in := map[string]any{"thread_id": threadID, "root": root}
	if strings.TrimSpace(originPartyID) != "" {
		in["origin_party_id"] = originPartyID
	}
	var r struct {
		RootID string `json:"root_id"`
		Count  int    `json:"count"`
	}
	if e := c.do("POST", "/api/v1/work-items/tree", in, &r); e != nil {
		return "", 0, e
	}
	return r.RootID, r.Count, nil
}

// SetRunPaused (Slice 2) reports a run PAUSED at needs_approval with an optional resume-at time:
// a rate-limited run carries its quota-window reset so the web can offer "resume at reset" and show
// when. Zero resumeAt omits it (a plain pause). Same device-token auth + owning-daemon guard as
// SetRunStatus — it POSTs the same run-status route, just with the extra resume_at field.
func SetRunPaused(base, token, runID, detail string, resumeAt time.Time) error {
	return SetRunPausedReason(base, token, runID, "", detail, resumeAt)
}

// SetRunPausedReason is SetRunPaused with the WHY attached (Epic G.2). reason is a
// surface.PauseReason key; empty is accepted and means "not stated", which the control plane
// renders as the old generic card rather than guessing — a wrong guess is the bug being fixed.
//
// The distinction that earns this a parameter: a rate_limit pause needs NO human action, because
// auto-resume clears it at resume_at, while an entitlement pause cannot be cleared by waiting at
// all. Rendering those two the same way is how an operator ends up watching a countdown to a
// moment that never arrives.
func SetRunPausedReason(base, token, runID, reason, detail string, resumeAt time.Time) error {
	in := map[string]any{"status": "needs_approval"}
	if detail != "" {
		in["detail"] = detail
	}
	if reason != "" {
		in["pause_reason"] = reason
	}
	if !resumeAt.IsZero() {
		in["resume_at"] = resumeAt.UTC().Format(time.RFC3339)
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID, in, nil)
}

// UpsertRunTask (O.3) reports one task's lifecycle transition to the per-TASK store, keyed by
// (run_id, idx). crank calls it as it processes the worklist — queued up front, then running
// before each task and done/failed after. Device-token auth (the owning daemon exposes its base
// + token to the crank child via env). Best-effort telemetry: crank logs + continues on error.
// RunTaskUpdate is one per-task lifecycle report (O.3 + #263). Idx + Status are always sent;
// the rest are omitted when empty/zero so a lifecycle-only transition (queued/running) stays
// minimal and a terminal report (done/failed) carries the legibility detail: the worker's own
// Summary, Tokens spent, and DurationMs (#263).
type RunTaskUpdate struct {
	Idx             int
	Task            string
	Status          string
	Branch          string
	Detail          string
	PRURL           string
	Summary         string
	Tokens          int     // O.5 ceiling signal — Total (incl. cache reads); over-counts, not displayed
	FreshTokens     int     // DISPLAY spend: input+output (new I/O only; excludes cached context)
	CacheReadTokens int     // cache_read only — a muted "+N cached" detail
	CostUSD         float64 // claude's total_cost_usd for this task (0 = not reported)
	DurationMs      int
	Verified        string // Trust · T2a: "" (no checks), "pass", or "fail" — recorded in the ledger payload
	ResumeHandle    string // Slice 2: engine's opaque resume token for this task ("" = restart-only)
	// Slice A2 conflict detection. ConflictsChecked distinguishes "scan ran, found none" (send [],
	// clearing any stored snapshot) from "scan couldn't run" (send nothing — the control plane keeps
	// its prior knowledge rather than being told 'clean' by a scan that never happened).
	Conflicts        []PRConflict
	ConflictsChecked bool
	// G.3: the full typed gate report for this task. The control plane derives `done` from it — a
	// worker reporting done against a failing gate is recorded as blocked instead. Empty on an
	// ungated repo, which the server records as UNVERIFIED rather than as a pass.
	Gate any
}

// PRConflict is one REAL merge conflict (git merge-tree, not same-file overlap) between this task's
// PR branch and another open PR. Resolvable = the other head is partyline-owned (crank-*) and thus
// ours to rebase; a human's PR is surfaced info-only.
type PRConflict struct {
	PR         int      `json:"pr"`
	Branch     string   `json:"branch"`
	Files      []string `json:"files"`
	Resolvable bool     `json:"resolvable"`
}

func UpsertRunTask(base, token, runID string, tr RunTaskUpdate) error {
	in := map[string]any{"idx": tr.Idx, "status": tr.Status}
	if tr.Task != "" {
		in["task"] = tr.Task
	}
	if tr.Branch != "" {
		in["branch"] = tr.Branch
	}
	if tr.Detail != "" {
		in["detail"] = tr.Detail
	}
	if tr.PRURL != "" {
		in["pr_url"] = tr.PRURL
	}
	if tr.Summary != "" {
		in["summary"] = tr.Summary
	}
	if tr.Tokens > 0 {
		in["tokens"] = tr.Tokens
	}
	if tr.FreshTokens > 0 {
		in["fresh_tokens"] = tr.FreshTokens
	}
	if tr.CacheReadTokens > 0 {
		in["cache_read_tokens"] = tr.CacheReadTokens
	}
	if tr.CostUSD > 0 {
		in["cost_usd"] = tr.CostUSD
	}
	if tr.DurationMs > 0 {
		in["duration_ms"] = tr.DurationMs
	}
	if tr.ResumeHandle != "" {
		in["resume_handle"] = tr.ResumeHandle
	}
	if tr.ConflictsChecked {
		c := tr.Conflicts
		if c == nil {
			c = []PRConflict{} // "checked, none" must serialize as [] — null means "not reported"
		}
		in["conflicts"] = c
	}
	// G.3: the typed gate report. The control plane derives `done` from it — a worker reporting
	// done against a failing gate is recorded as blocked instead.
	if tr.Gate != nil {
		in["gate"] = tr.Gate
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/tasks", in, nil)
}

// RunLogEvent is one link in a daemon's tamper-evident chain for a run (TRUST · T1). The daemon
// computes Seq/PrevHash/Hash locally (it's the chain authority) and appends; the server derives
// daemon_id from the device token and enforces continuity (Seq == last+1, PrevHash == last hash).
// TaskIdx is a pointer so a run-level event (nil) is distinct from task idx 0. Payload is the same
// report body as the run_tasks projection. (Distinct from RunEvent, which is the stream run-profile.)
type RunLogEvent struct {
	Seq      int            `json:"seq"`
	PrevHash string         `json:"prev_hash"`
	Hash     string         `json:"hash"`
	Kind     string         `json:"kind"`
	TaskIdx  *int           `json:"task_idx"`
	Payload  map[string]any `json:"payload"`
}

// AppendRunEvent appends one chained event to a run's ledger (device-token auth, owning-daemon
// guard + continuity check server-side). Best-effort telemetry like UpsertRunTask: a rejected
// append (continuity mismatch, transient error) is logged + swallowed by the caller and never
// affects the run. The server rejects a gap/fork with 409 so a mis-seeded chain fails loud.
func AppendRunEvent(base, token, runID string, ev RunLogEvent) error {
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/events", ev, nil)
}

// RunLogLine is one line of the worker's LIVE STEP OUTPUT (crank-01). This is the HIGH-VOLUME,
// NON-chained companion to RunLogEvent (the milestone ledger): crank streams the worker's stdout/steps
// here as a task runs, and the run detail page tails them live over Realtime. Deliberately separate
// from the tamper-evident chain — logs would bloat it (see 0055_run_logs). Seq is a producer-assigned
// ordering hint (one crank process = one daemon on one run), NOT a security artifact.
type RunLogLine struct {
	TaskIdx *int   `json:"task_idx"` // nil = run-level; else the run_tasks.idx this line is about
	Seq     int64  `json:"seq"`
	Stream  string `json:"stream"` // "stdout" | "stderr" | "step"
	Body    string `json:"body"`
}

// AppendRunLogs posts a BATCH of the worker's step-output lines to the run's log stream (device-token
// auth, owning-daemon guard server-side). Best-effort telemetry like AppendRunEvent: a dropped batch is
// swallowed by the caller and never affects the run. Batched (crank flushes on a timer) so the
// high-volume stream stays cheap.
func AppendRunLogs(base, token, runID string, lines []RunLogLine) error {
	if len(lines) == 0 {
		return nil
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/logs", map[string]any{"lines": lines}, nil)
}

// LastRunEvent returns this daemon's current chain head for a run — {seq, hash} of its last
// appended event, or {0, ""} if it has none (fresh chain). crank calls it once at startup to
// SEED the chain so a --resume (or a relaunched worker) continues the daemon's existing chain
// instead of colliding at seq 0. Best-effort: on error crank starts fresh (0, "").
func LastRunEvent(base, token, runID string) (seq int, hash string, err error) {
	var out struct {
		Seq  int    `json:"seq"`
		Hash string `json:"hash"`
		None bool   `json:"none"`
	}
	if err = daemonDo("GET", base, token, "/api/v1/daemon/run/"+runID+"/events/head", nil, &out); err != nil {
		return 0, "", err
	}
	if out.None {
		return 0, "", nil
	}
	return out.Seq + 1, out.Hash, nil // resume at head+1, chaining onto head's hash
}

// RunTaskStatus is one task's stored lifecycle state (O.3), keyed by idx within the run — the
// minimum crank --resume (#81 slice 3a) needs to skip already-`done` tasks without re-running them.
type RunTaskStatus struct {
	Idx          int    `json:"idx"`
	Status       string `json:"status"`
	ResumeHandle string `json:"resume_handle"` // Slice 2: engine's resume token for this task ("" = restart)
	Detail       string `json:"detail"`        // why it stopped — for a quarantined task, the reviewer's findings
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
// leaseSeconds (#213) is derived by the caller from the worker's per-task timeout so the claim's
// lease outlasts the task — a slow-but-alive task must not have its lease expire and get reclaimed.
func ClaimNextTask(base, token, runID string, leaseSeconds int) (*ClaimedTask, error) {
	var out struct {
		Task *ClaimedTask `json:"task"`
	}
	in := map[string]any{"lease_seconds": leaseSeconds}
	if err := daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/claim", in, &out); err != nil {
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
	// A transcript read is idempotent — retry transient failures so a blip doesn't cost a turn.
	res, err := p.doWithRetry("GET", p.Base+"/api/v1/parties/"+p.ID+"/messages?format=markdown", nil, 30*time.Second)
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

// TokenPath is tokenPath for callers outside the package — so a CLI message can name the file it
// actually wrote instead of assuming production. See ConfigDir: the path is per-control-plane.
func TokenPath() string { return tokenPath() }

// ---- triggers: configuring the account from the CLI (#818 follow-on) --------------------------
//
// The point of these is that an LLM with a shell can set partyline up. `ptln login` already put a
// token on this machine, so the CLI can act as the user without a browser, a device flow, or a
// human clicking through Settings.
//
// This grants no new power. An agent with the user's shell could already curl the same API with the
// same token. What it changes is that the operations become NAMED, VALIDATED and AUDITABLE instead
// of hand-rolled — which is strictly safer than the alternative of an agent improvising HTTP
// against a spec it read in the docs.

type Target struct {
	DaemonID    string   `json:"daemon_id"`
	DeviceLabel string   `json:"device_label"`
	Online      bool     `json:"online"`
	Projects    []string `json:"projects"`
}

// ListTargets returns the machines this account can run work on and what each advertises.
func (c *Client) ListTargets() ([]Target, error) {
	var r struct {
		Targets []Target `json:"targets"`
	}
	if err := c.do("GET", "/api/v1/targets", nil, &r); err != nil {
		return nil, err
	}
	return r.Targets, nil
}

type Trigger struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Gate        string   `json:"gate"`
	Preset      string   `json:"preset"`
	Enabled     bool     `json:"enabled"`
	FireCount   int      `json:"fire_count"`
	LastFiredAt string   `json:"last_fired_at"`
	ActOn       []string `json:"act_on"`
	OutcomePath string   `json:"outcome_path"`
	SuccessWhen string   `json:"success_when"`
	// The instruction the agent is actually given. Absent from this struct until #854, which is why
	// a misconfigured trigger was hard to see: `ptln trigger ls --json` showed no template even when
	// one was stored, so the output could not be used to check a setup.
	TaskTemplate string `json:"task_template"`
	// The persona attached to this trigger, for the same reason task_template is here: a setting you
	// cannot READ is a setting you cannot check. `ptln trigger set --template` could attach one and
	// no read anywhere in the CLI would show it — the server has always returned it.
	AgentTemplateID string `json:"agent_template_id"`
}

// TriggerPatch carries only the fields being CHANGED. A nil pointer means "leave it alone", which is
// what lets a caller fix one thing without blanking the rest — and what makes it safe to correct a
// trigger in place instead of deleting it and minting a new key.
type TriggerPatch struct {
	Name         *string   `json:"name,omitempty"`
	TaskTemplate *string   `json:"task_template,omitempty"`
	Gate         *string   `json:"gate,omitempty"`
	ActOn        *[]string `json:"act_on,omitempty"`
	OutcomePath  *string   `json:"outcome_path,omitempty"`
	SuccessWhen  *string   `json:"success_when,omitempty"`
	// A pointer to "" DETACHES the persona; nil leaves it alone.
	AgentTemplateID *string `json:"agent_template_id,omitempty"`
}

func (c *Client) UpdateTrigger(id string, patch TriggerPatch) error {
	return c.do("PATCH", "/api/v1/triggers/"+id, patch, nil)
}

func (c *Client) ListTriggers() ([]Trigger, error) {
	var r struct {
		Triggers []Trigger `json:"triggers"`
	}
	if err := c.do("GET", "/api/v1/triggers", nil, &r); err != nil {
		return nil, err
	}
	return r.Triggers, nil
}

type NewTrigger struct {
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	ProjectLabel string   `json:"project_label"`
	DaemonID     string   `json:"daemon_id"`
	TaskTemplate string   `json:"task_template"`
	Gate         string   `json:"gate,omitempty"`
	Preset       string   `json:"preset,omitempty"`
	ActOn        []string `json:"act_on,omitempty"`
	OutcomePath  string   `json:"outcome_path,omitempty"`
	SuccessWhen  string   `json:"success_when,omitempty"`
	// AgentTemplateID attaches a reusable persona. Empty means the inline task alone.
	AgentTemplateID string `json:"agent_template_id,omitempty"`
}

// CreateTrigger makes an inbound entry point and returns it with its key.
//
// The key comes back ONCE and is never retrievable again — same contract as the web UI, because the
// value of "shown once" is entirely lost if a second call can fetch it.
func (c *Client) CreateTrigger(in NewTrigger) (*Trigger, string, error) {
	var r struct {
		Trigger  Trigger `json:"trigger"`
		Key      string  `json:"key"`
		KeyError string  `json:"key_error"`
	}
	if err := c.do("POST", "/api/v1/triggers", in, &r); err != nil {
		return nil, "", err
	}
	if r.KeyError != "" {
		// The trigger exists either way; a key that failed to mint is fixable, not a reason to
		// pretend the whole thing failed.
		return &r.Trigger, "", fmt.Errorf("trigger created, but no key was issued: %s", r.KeyError)
	}
	return &r.Trigger, r.Key, nil
}

func (c *Client) SetTriggerEnabled(id string, on bool) error {
	return c.do("PATCH", "/api/v1/triggers/"+id, map[string]bool{"enabled": on}, nil)
}

func (c *Client) DeleteTrigger(id string) error {
	return c.do("DELETE", "/api/v1/triggers/"+id, nil, nil)
}

// TriggerBucket is one day of one trigger's activity, counted by outcome. The map keys are the same
// words the dashboard's chart bands use ("started work", "duplicate", …) — one vocabulary, so the
// CLI and the web page can never describe the same day differently.
type TriggerBucket struct {
	Date   string         `json:"date"`
	Counts map[string]int `json:"counts"`
}

// TriggerSeries is one trigger's shape over the window. Buckets are DENSE — every day in the
// window is present, including the empty ones — so a caller can index them positionally.
type TriggerSeries struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Detail  string          `json:"detail"`
	Total   int             `json:"total"`
	LastAt  string          `json:"lastAt"`
	Buckets []TriggerBucket `json:"buckets"`
	// Inactive explains a series that stops dead — "turned off" rather than a mystery flat line.
	Inactive string `json:"inactive"`
}

// TriggerActivity reads what every trigger has been doing over the last `days`.
//
// This is the same read the dashboard's Triggers panel draws. It exists as a command because a
// number a human can only get by looking at a chart is a number an agent cannot act on.
func (c *Client) TriggerActivity(days int) ([]TriggerSeries, int, error) {
	path := "/api/v1/triggers/activity"
	if days > 0 {
		path += "?days=" + strconv.Itoa(days)
	}
	var r struct {
		WindowDays int             `json:"window_days"`
		Triggers   []TriggerSeries `json:"triggers"`
	}
	if err := c.do("GET", path, nil, &r); err != nil {
		return nil, 0, err
	}
	return r.Triggers, r.WindowDays, nil
}

// TriggerEvent is one inbound call: when, what it did, and the run it started if it started one.
type TriggerEvent struct {
	ID      int64  `json:"id"`
	At      string `json:"at"`
	Outcome string `json:"outcome"`
	Ref     string `json:"ref"`
	Skipped string `json:"skipped"`
	RunID   string `json:"run_id"`
	// RunStatus/RunTitle are blank when no run started, or when the run is not readable.
	RunStatus string `json:"run_status"`
	RunTitle  string `json:"run_title"`
}

// TriggerEvents reads one trigger's event log. `idOrSlug` is either — the route resolves both, so a
// caller never has to run a lookup to turn the slug they chose into an id they did not.
func (c *Client) TriggerEvents(idOrSlug string, limit int) (*Trigger, []TriggerEvent, bool, error) {
	path := "/api/v1/triggers/" + url.PathEscape(idOrSlug) + "/events"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var r struct {
		Trigger   Trigger        `json:"trigger"`
		Events    []TriggerEvent `json:"events"`
		Truncated bool           `json:"truncated"`
	}
	if err := c.do("GET", path, nil, &r); err != nil {
		return nil, nil, false, err
	}
	return &r.Trigger, r.Events, r.Truncated, nil
}

// RunCheckPolicy is the control plane's say about ONE named acceptance check (G.4 project_checks).
// Name matches the repo's own; enabled/blocking/path_glob are the policy. No command — see the
// RunEvent.Checks comment for why that absence is load-bearing rather than an oversight.
type RunCheckPolicy struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Blocking bool   `json:"blocking"`
	PathGlob string `json:"path_glob,omitempty"`
}

// RunReviewLane is one reviewer lane (G.5 project_review_lanes): which engine to ask, with which
// model. Empty model = the engine's own default. The daemon re-checks the engine against the set it
// can actually spawn, because the control plane does not know what is installed on this machine.
type RunReviewLane struct {
	ID     string `json:"id"`
	Engine string `json:"engine"`
	Model  string `json:"model,omitempty"`
}

// ── CLI planning mode (docs/epics/cli-planning-mode.md) ──────────────────────────────────────────

// SpecCheck is one requirement the specificity gate evaluates. Mirrors the server's shape so the
// wording a planning session shows the user is the SERVER's wording — if these ever disagree, the
// model is told it is finished by one definition and refused by another at Start.
type SpecCheck struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
}

// Specificity is the gate's verdict on a draft. `Message` is the rendered instructions — the server
// already phrases the blocking checks as things to DO, which is what a model needs.
type Specificity struct {
	OK         bool        `json:"ok"`
	Score      int         `json:"score"`
	Checks     []SpecCheck `json:"checks"`
	Blocking   []SpecCheck `json:"blocking"`
	Message    string      `json:"message"`
	MaxTaskLen int         `json:"max_task_len"`
}

// CheckSpecificity dry-runs the gate against a draft that does not exist yet.
//
// The CLI ASKS rather than reimplementing: computeSpecificity is TypeScript, cg-mcp is Go, and a
// second copy of these checks is the exact failure this epic exists to fix — the CLI and web
// planning agents already drifted once because the persona was copied instead of served.
func (c *Client) CheckSpecificity(title, document string, criteria []WorkItemCriterion) (Specificity, error) {
	in := map[string]any{"title": title, "document": document, "acceptance_criteria": criteria}
	var out Specificity
	if e := c.do("POST", "/api/v1/work-items/specificity", in, &out); e != nil {
		return Specificity{}, e
	}
	return out, nil
}

// Persona fetches an agent persona from the control plane so the CLI and the web run the SAME
// planning agent. Callers fall back to their embedded copy on any error — a session on a plane
// should degrade to a slightly older persona, never to no persona.
func (c *Client) Persona(mode string) (string, error) {
	var out struct {
		Preamble string `json:"preamble"`
	}
	if e := c.do("GET", "/api/v1/personas/"+mode, nil, &out); e != nil {
		return "", e
	}
	return out.Preamble, nil
}

// PromoteWorkItem enqueues a filed task (or a container's task leaves) as a run — the verb that
// makes a CLI planning conversation reach a running crank job without a browser.
//
// Wraps the SAME endpoints the web board's Start uses, so authz, the specificity gate and the
// readiness rules are the server's, not a second CLI-side opinion. `container` picks promote-tree
// (a whole decomposition as one ordered chain) over promote (a single task).
func (c *Client) PromoteWorkItem(itemID, daemonID, projectLabel, mergePolicy string, container bool) (string, error) {
	in := map[string]any{"daemon_id": daemonID, "project_label": projectLabel}
	if mergePolicy != "" {
		in["merge_policy"] = mergePolicy
	}
	path := "/api/v1/work-items/" + itemID + "/promote"
	if container {
		path += "-tree"
	}
	var out struct {
		RunID   string `json:"run_id"`
		ChainID string `json:"chain_id"`
		Count   int    `json:"count"`
	}
	if e := c.do("POST", path, in, &out); e != nil {
		return "", e
	}
	if out.RunID != "" {
		return out.RunID, nil
	}
	return out.ChainID, nil
}

// AssertKey fetches this instance's identity-assertion PUBLIC key (raw Ed25519, base64-std) plus
// its fingerprint. Unauthenticated by design: it is a public key, and `ptln login <url>` needs it
// BEFORE there is a token. Returns ok=false (no error) when the instance does not serve the
// endpoint or has no signing key configured — an older or identity-less instance is a normal
// state, not a failure, and must not make a successful login look failed.
//
// TLS is what authenticates this answer: an attacker needs a valid certificate for that domain to
// substitute a key here. The caller compares it against the pinned root (identity.CheckOfferedKey).
func (c *Client) AssertKey() (key, fingerprint string, ok bool, err error) {
	req, err := http.NewRequest("GET", c.Base+"/api/v1/identity/key", nil)
	if err != nil {
		return "", "", false, err
	}
	hc := &http.Client{Timeout: 8 * time.Second}
	res, err := hc.Do(req)
	if err != nil {
		return "", "", false, err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 || res.StatusCode == 501 {
		return "", "", false, nil // instance doesn't publish one
	}
	if res.StatusCode != 200 {
		return "", "", false, fmt.Errorf("identity key: status %d", res.StatusCode)
	}
	var v struct {
		Key         string `json:"key"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(v.Key) == "" {
		return "", "", false, nil
	}
	return strings.TrimSpace(v.Key), strings.TrimSpace(v.Fingerprint), true, nil
}
