package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// ErrSkillNoBundle is returned by GetSkillBundle when a skill has no packaged bundle (the server 404s
// the /bundle route because the skill is body-only). Callers fall back to GetSkill's SKILL.md body.
var ErrSkillNoBundle = errors.New("skill has no bundle")

// maxBundleBytes bounds a downloaded bundle read defensively; the real per-file/total caps live in
// internal/skillzip and are enforced at unpack time. Kept a touch above the bundler's 20 MiB total.
const maxBundleBytes = 24 << 20

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
	// HasBundle is true when this skill version ships a packaged BUNDLE (scripts/references/assets),
	// not just the SKILL.md body. The daemon uses it to decide whether to fetch the full zip
	// (GetSkillBundleForDaemon) and materialize the whole tree, vs writing body-only SKILL.md.
	HasBundle bool `json:"has_bundle"`
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
	// The endpoint returns {"skills": [...]}, not a bare array. Decoding into []SkillMeta made
	// `ptln skill list` fail with a json type error for every user who had a skill — the whole
	// read side of the skill library was unusable from the CLI.
	var out struct {
		Skills []SkillMeta `json:"skills"`
	}
	if err := c.do("GET", "/api/v1/skills", nil, &out); err != nil {
		return nil, err
	}
	return out.Skills, nil
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

// PushSkillBundle creates a skill (or a new version) from a packaged BUNDLE: a multipart/form-data POST
// to /api/v1/skills carrying the whole skill dir as a `bundle` zip, plus name/description form fields
// (which override the SKILL.md frontmatter the server parses from the zip). Returns the stored name +
// new version. Uses raw net/http because Client.do is JSON-only.
func (c *Client) PushSkillBundle(name, description string, zip []byte) (string, int, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if name != "" {
		_ = mw.WriteField("name", name)
	}
	if description != "" {
		_ = mw.WriteField("description", description)
	}
	fw, err := mw.CreateFormFile("bundle", name+".zip")
	if err != nil {
		return "", 0, err
	}
	if _, err := fw.Write(zip); err != nil {
		return "", 0, err
	}
	if err := mw.Close(); err != nil {
		return "", 0, err
	}
	req, err := http.NewRequest("POST", c.Base+"/api/v1/skills", &body)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", 0, err
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
		return "", 0, fmt.Errorf("%s", e.Error)
	}
	var out struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.Name, out.Version, nil
}

// GetSkillBundle streams a packaged skill's zip from GET /api/v1/skills/<name>/bundle (latest version;
// pass version>0 to pin one). Returns ErrSkillNoBundle on a 404 so pull/install can fall back to the
// body-only SKILL.md path. Raw net/http (the response is a zip, not JSON).
func (c *Client) GetSkillBundle(name string, version int) ([]byte, error) {
	u := c.Base + "/api/v1/skills/" + name + "/bundle"
	if version > 0 {
		u += fmt.Sprintf("?version=%d", version)
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrSkillNoBundle
	}
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
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBundleBytes {
		return nil, fmt.Errorf("skill bundle for %q is too large (over %d bytes)", name, maxBundleBytes)
	}
	return data, nil
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

// GetSkillBundleForDaemon fetches a packaged skill's full zip via the DAEMON route
// (GET /api/v1/daemon/skills/<name>/bundle, device token). Returns ErrSkillNoBundle on a 404 so the
// caller falls back to body-only materialization. Raw net/http (the response is a zip, not JSON); the
// read is bounded by maxBundleBytes — the real per-file/total caps are re-enforced at unpack time
// (internal/skillzip), the trust boundary that writes to the worktree.
func GetSkillBundleForDaemon(base, token, name string) ([]byte, error) {
	req, err := http.NewRequest("GET", base+"/api/v1/daemon/skills/"+name+"/bundle", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrSkillNoBundle
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch skill bundle %q: %s", name, res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBundleBytes {
		return nil, fmt.Errorf("skill bundle for %q is too large (over %d bytes)", name, maxBundleBytes)
	}
	return data, nil
}

// SkillRef is one skill's identity for usage telemetry — the name + the version that was injected.
type SkillRef struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// ReportSkillUsage tells the control plane which skills the daemon INJECTED into a run's workspace
// (POST /api/v1/daemon/run/<runID>/skill-usage, device token). This is the reliable cheap-tier signal
// powering "injected into N runs" in the library — invocation (did the agent actually use it?) is a
// separate best-effort signal the server flips later. Best-effort at the call site: a failure logs and
// the run proceeds (telemetry must never break a build).
func ReportSkillUsage(base, token, runID string, injected []SkillRef) error {
	if len(injected) == 0 {
		return nil
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/skill-usage",
		map[string]any{"injected": injected}, nil)
}

// ReportSkillInvocation flips the injected rows the agent actually USED to invoked=true (same endpoint,
// `invoked` array). This is the best-effort, engine-dependent signal: the crank worker derives it from
// claude's stream-json tool events (a skill activation or a read of the skill's files); buffered engines
// report nothing, so their skills stay invoked=false — an under-count, never an over-count. The server
// matches by run+name and ignores version on the flip, so version here only backstops a race-insert.
func ReportSkillInvocation(base, token, runID string, invoked []SkillRef) error {
	if len(invoked) == 0 {
		return nil
	}
	return daemonDo("POST", base, token, "/api/v1/daemon/run/"+runID+"/skill-usage",
		map[string]any{"invoked": invoked}, nil)
}
