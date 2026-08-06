package api

import "fmt"

// Settings the CLI can change — the API half of "every feature has a CLI path" (#829).
//
// Kept separate from client.go because these are all the same shape (read a settings surface, write
// it back) and because client.go is already long. Nothing here is new capability: `ptln login` put a
// token on this machine, and these endpoints have always accepted it. What was missing was a NAME
// for each operation, so an agent had to improvise HTTP from a doc instead of calling something
// validated and consistent.

// ── webhooks (outbound: where a team's events go) ────────────────────────────────────────────────

type Webhook struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Kinds     []string `json:"kinds"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"created_at"`
}

// ListWebhooks also returns the closed set of event kinds the server accepts, so a caller can offer
// them rather than guess — an agent that guesses a kind gets a silent empty filter.
func (c *Client) ListWebhooks() ([]Webhook, []string, error) {
	var out struct {
		Endpoints []Webhook `json:"endpoints"`
		Kinds     []string  `json:"kinds"`
	}
	if err := c.do("GET", "/api/v1/webhooks", nil, &out); err != nil {
		return nil, nil, err
	}
	return out.Endpoints, out.Kinds, nil
}

// CreateWebhook returns the signing secret, which the server shows ONCE. The caller is responsible
// for putting it somewhere before it is lost — see the --key-only path in webhook_cmds.go.
func (c *Client) CreateWebhook(name, url string, kinds []string) (id, secret string, err error) {
	if kinds == nil {
		kinds = []string{}
	}
	var out struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	err = c.do("POST", "/api/v1/webhooks", map[string]any{"name": name, "url": url, "kinds": kinds}, &out)
	return out.ID, out.Secret, err
}

func (c *Client) DeleteWebhook(id string) error {
	return c.do("DELETE", "/api/v1/webhooks/"+id, nil, nil)
}

// ── credentials (API keys) ───────────────────────────────────────────────────────────────────────

type Credential struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	CreatedAt string   `json:"created_at"`
	LastUsed  string   `json:"last_used_at"`
	ExpiresAt string   `json:"expires_at"`
	RevokedAt string   `json:"revoked_at"`
}

func (c *Client) ListCredentials() ([]Credential, error) {
	var out struct {
		Credentials []Credential `json:"credentials"`
	}
	err := c.do("GET", "/api/v1/credentials", nil, &out)
	return out.Credentials, err
}

// CreateCredential mints a key. Like a webhook secret, the raw value comes back exactly once.
func (c *Client) CreateCredential(name string, scopes []string, expiresAt string) (id, secret string, err error) {
	body := map[string]any{"kind": "org_key", "name": name}
	if len(scopes) > 0 {
		body["scopes"] = scopes
	}
	if expiresAt != "" {
		body["expires_at"] = expiresAt
	}
	var out struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	err = c.do("POST", "/api/v1/credentials", body, &out)
	return out.ID, out.Secret, err
}

func (c *Client) RevokeCredential(id string) error {
	return c.do("DELETE", "/api/v1/credentials/"+id, nil, nil)
}

// ── profile ──────────────────────────────────────────────────────────────────────────────────────

// UpdateMe takes only the fields present in the map, matching the endpoint's own patch semantics —
// an absent key is untouched, not cleared.
func (c *Client) UpdateMe(patch map[string]any) error {
	return c.do("PATCH", "/api/v1/me", patch, nil)
}

// ── notification preferences ─────────────────────────────────────────────────────────────────────

// NotifyPrefs mirrors the endpoint exactly: a grid of event → channel → on, plus quiet hours. The
// grid is already resolved (stored preferences over defaults, mandatory floors forced on), so a
// caller renders what it is given rather than reimplementing the defaulting.
type NotifyPrefs struct {
	Prefs          map[string]map[string]bool `json:"prefs"`
	QuietStart     string                     `json:"quiet_start"`
	QuietEnd       string                     `json:"quiet_end"`
	SlackConnected bool                       `json:"slack_connected"`
}

func (c *Client) NotifyPrefs() (*NotifyPrefs, error) {
	var out NotifyPrefs
	err := c.do("GET", "/api/v1/me/notifications", nil, &out)
	return &out, err
}

func (c *Client) SetNotifyPref(event, channel string, on bool) error {
	return c.do("PATCH", "/api/v1/me/notifications",
		map[string]any{"prefs": map[string]any{event: map[string]any{channel: on}}}, nil)
}

func (c *Client) SetQuietHours(start, end string) error {
	return c.do("PATCH", "/api/v1/me/notifications", map[string]any{"quiet_start": start, "quiet_end": end}, nil)
}

// ── project document (the globals injected into every run) ───────────────────────────────────────

type ProjectDoc struct {
	Body      string `json:"body"`
	UpdatedAt string `json:"updated_at"`
}

func (c *Client) ProjectDocument(projectID string) (*ProjectDoc, error) {
	var out ProjectDoc
	err := c.do("GET", "/api/v1/projects/"+projectID+"/document", nil, &out)
	return &out, err
}

func (c *Client) SetProjectDocument(projectID, body string) error {
	return c.do("PUT", "/api/v1/projects/"+projectID+"/document", map[string]any{"body": body}, nil)
}

// ── project environments (the deploy chain) ──────────────────────────────────────────────────────

type Environment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Branch   string `json:"branch"`
	Position int    `json:"position"`
}

func (c *Client) Environments(projectID string) ([]Environment, error) {
	var out struct {
		Environments []Environment `json:"environments"`
	}
	err := c.do("GET", "/api/v1/projects/"+projectID+"/environments", nil, &out)
	return out.Environments, err
}

// SetEnvironments replaces the whole chain, because that is what the endpoint does — order is
// meaningful (it IS the promotion sequence), so there is no coherent "add one" that does not also
// say where it goes.
func (c *Client) SetEnvironments(projectID string, envs []Environment) error {
	list := make([]map[string]any, 0, len(envs))
	for _, e := range envs {
		list = append(list, map[string]any{"name": e.Name, "branch": e.Branch})
	}
	return c.do("PUT", "/api/v1/projects/"+projectID+"/environments", map[string]any{"environments": list}, nil)
}

// ── team settings ────────────────────────────────────────────────────────────────────────────────

func (c *Client) UpdateOrgSettings(slug string, patch map[string]any) error {
	if len(patch) == 0 {
		return fmt.Errorf("nothing to change")
	}
	return c.do("PATCH", "/api/v1/orgs/"+slug, patch, nil)
}

// ── skills ───────────────────────────────────────────────────────────────────────────────────────

func (c *Client) DeleteSkill(name string) error {
	return c.do("DELETE", "/api/v1/skills/"+name, nil, nil)
}

// ── agent templates (reusable personas a trigger can wake) ───────────────────────────────────────

type AgentTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	StopRule  string `json:"stop_rule"`
	Body      string `json:"body"`
	UpdatedAt string `json:"updated_at"`
}

func (c *Client) ListTemplates() ([]AgentTemplate, error) {
	var out struct {
		Templates []AgentTemplate `json:"templates"`
	}
	err := c.do("GET", "/api/v1/agent-templates", nil, &out)
	return out.Templates, err
}

// CreateTemplate authors a persona. `status` decides whether a trigger will actually use it — a
// draft is deliberately ignored at fire time, so an unfinished instruction cannot wake an agent.
func (c *Client) CreateTemplate(name, body, stopRule, status string) (string, error) {
	in := map[string]any{"name": name, "body": body}
	if stopRule != "" {
		in["stop_rule"] = stopRule
	}
	if status != "" {
		in["status"] = status
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do("POST", "/api/v1/agent-templates", in, &out)
	return out.ID, err
}

// ── chat transports (docs/epics/chat-transports.md) ───────────────────────────────────────────

type ChatIdentity struct {
	Platform    string `json:"platform"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

type ChatLinked struct {
	Linked []ChatIdentity `json:"linked"`
}

type ChatLinkCode struct {
	Code             string `json:"code"`
	ExpiresInMinutes int    `json:"expires_in_minutes"`
	Send             string `json:"send"`
	Where            string `json:"where"`
}

func (c *Client) ChatLinked() (*ChatLinked, error) {
	var out ChatLinked
	err := c.do("GET", "/api/v1/chat/link", nil, &out)
	return &out, err
}

func (c *Client) ChatLinkCode(platform string) (*ChatLinkCode, error) {
	var out ChatLinkCode
	err := c.do("POST", "/api/v1/chat/link", map[string]any{"platform": platform}, &out)
	return &out, err
}

func (c *Client) ChatUnlink(platform string) error {
	return c.do("DELETE", "/api/v1/chat/link", map[string]any{"platform": platform}, nil)
}
