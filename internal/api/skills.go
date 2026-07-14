package api

// Org-level skill library (v1). A "skill" is the Agent Skills open standard: a <name>/SKILL.md whose
// YAML frontmatter carries name+description and whose markdown body is the instructions. Skills are
// org-scoped and versioned server-side; the daemon injects the ENABLED ones into every agent run's
// workspace at launch so any engine can use them.

// Skill is one org skill as delivered to the DAEMON (device token, ENABLED only) for injection into a
// run's workspace. Body is the SKILL.md contents (frontmatter + markdown). Name is a dir-safe slug —
// the daemon re-validates it (gitwt.ValidSkillName) before it becomes a path.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Version     int    `json:"version"`
}

// SkillsForDaemon fetches this daemon's org ENABLED skills (device-token auth), for injection at run
// launch. Best-effort at the call site: the daemon logs a failure and proceeds with NO skills rather
// than failing the run — same posture as project globals.
func SkillsForDaemon(base, token string) ([]Skill, error) {
	var out struct {
		Skills []Skill `json:"skills"`
	}
	if err := daemonDo("GET", base, token, "/api/v1/daemon/skills", nil, &out); err != nil {
		return nil, err
	}
	return out.Skills, nil
}

// SkillMeta is one row of the user-token `ptln skill list` (no body — that's fetched on demand).
type SkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updated_at"`
}

// ListSkills lists the caller's org skills (user token).
func (c *Client) ListSkills() ([]SkillMeta, error) {
	var out []SkillMeta
	if err := c.do("GET", "/api/v1/skills", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PushSkill creates a skill or a NEW VERSION of an existing one (user token). Returns the stored name
// and the new version number.
func (c *Client) PushSkill(name, description, body string) (string, int, error) {
	var out struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	in := map[string]string{"name": name, "description": description, "body": body}
	if err := c.do("POST", "/api/v1/skills", in, &out); err != nil {
		return "", 0, err
	}
	return out.Name, out.Version, nil
}

// SkillVersion is one entry of a skill's version history.
type SkillVersion struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

// SkillDetail is the full skill (user token): metadata + the current body + version history.
type SkillDetail struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Body        string         `json:"body"`
	Version     int            `json:"version"`
	History     []SkillVersion `json:"history"`
}

// GetSkill fetches a skill's latest body + metadata by name (user token).
func (c *Client) GetSkill(name string) (*SkillDetail, error) {
	var out SkillDetail
	if err := c.do("GET", "/api/v1/skills/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
