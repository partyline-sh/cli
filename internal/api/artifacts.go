package api

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Planning artifacts — a versioned HTML worked example pinned to a work item, plus the typed markup
// a human leaves on it. This is the client half of /api/v1/work-items/[id]/artifacts.
//
// WHY THIS EXISTS. A crank worker has no browser and never sees rendered pixels, so it produces
// layout work that reads correct and is wrong. Prose cannot close that gap and a screenshot cannot
// either — what closes it is a mockup the human has drawn on, converted into acceptance criteria the
// worker can build against. `ptln review` is the surface that captures the drawing.

type Artifact struct {
	ID         string `json:"id"`
	WorkItemID string `json:"work_item_id"`
	Version    int    `json:"version"`
	Title      string `json:"title"`
	Note       string `json:"note"`
	SizeBytes  int64  `json:"size_bytes"`
	CreatedAt  string `json:"created_at"`
	AcceptedAt string `json:"accepted_at"`
	// Carried reports how many unresolved marks were copied forward from the previous version.
	// Only set on the publish response.
	Carried int `json:"carried"`
}

// Rect is an element box in artifact space (the artifact is laid out at full content height, so
// these coordinates do not move with scroll).
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Anchor is deliberately redundant: the selector survives a layout change, the rect survives a
// selector change, and whichever still resolves wins after a regeneration. Pixel-only anchoring
// breaks on every regeneration, which is what makes most overlay tools useless by the second pass.
type Anchor struct {
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Rect     *Rect  `json:"rect,omitempty"`
	Viewport string `json:"viewport,omitempty"` // mobile | tablet | desktop
}

// Shape is the drawn overlay when there is one. Points are artifact-space [x,y] pairs.
type Shape struct {
	Type   string       `json:"type"` // pin | rect | arrow | freehand | highlight
	Points [][2]float64 `json:"points"`
	Color  string       `json:"color,omitempty"`
	Stroke float64      `json:"stroke,omitempty"`
}

// Annotation is one typed mark. The server-assigned fields carry omitempty because this struct is
// also the POST body: a client has no id, artifact_id or fingerprint to offer, and sending empty
// ones would imply it does. fingerprint especially is computed server-side on purpose — it decides
// whether a mark is "the same complaint" across versions, so a client must not be able to set it.
type Annotation struct {
	ID          string `json:"id,omitempty"`
	ArtifactID  string `json:"artifact_id,omitempty"`
	WorkItemID  string `json:"work_item_id,omitempty"`
	Kind        string `json:"kind"` // scope | behaviour | constraint | question
	Body        string `json:"body"`
	Anchor      Anchor `json:"anchor"`
	Shape       *Shape `json:"shape,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	ResolvedAt  string `json:"resolved_at,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// PublishArtifact records a NEW VERSION of the worked example. Unresolved marks from the previous
// version are carried forward server-side, so regenerating never silently drops what the human is
// still objecting to.
func (c *Client) PublishArtifact(workItemID, html, title, note string) (*Artifact, error) {
	in := map[string]string{"html": html, "title": title, "note": note}
	var out Artifact
	if err := c.do("POST", "/api/v1/work-items/"+url.PathEscape(workItemID)+"/artifacts", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListArtifacts returns every version of a work item's worked example, newest first.
func (c *Client) ListArtifacts(workItemID string) ([]Artifact, error) {
	var r struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	if err := c.do("GET", "/api/v1/work-items/"+url.PathEscape(workItemID)+"/artifacts", nil, &r); err != nil {
		return nil, err
	}
	return r.Artifacts, nil
}

// FetchArtifact returns an artifact's raw HTML bytes.
//
// These bytes are UNTRUSTED: an agent wrote them, and a prompt injection in the repo can steer what
// the generator emits. The server refuses to serve them inline for that reason. Every caller must
// render them inside a sandbox — see the viewer in review.go, which mounts them with
// sandbox="allow-scripts" and no allow-same-origin.
func (c *Client) FetchArtifact(workItemID, artifactID string) ([]byte, error) {
	path := "/api/v1/work-items/" + url.PathEscape(workItemID) + "/artifacts/" + url.PathEscape(artifactID)
	req, err := http.NewRequest("GET", c.Base+path, nil)
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
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch artifact: %s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}

// PostMarks records a batch of typed marks against one artifact version. Returns how many were
// accepted — the server drops anything whose kind is not in the closed vocabulary rather than
// coercing it, so a mismatch here is a real signal, not a rounding error.
func (c *Client) PostMarks(workItemID, artifactID string, marks []Annotation) (int, error) {
	in := map[string]any{"marks": marks}
	var out struct {
		Recorded int `json:"recorded"`
	}
	path := "/api/v1/work-items/" + url.PathEscape(workItemID) + "/artifacts/" + url.PathEscape(artifactID) + "/annotations"
	if err := c.do("POST", path, in, &out); err != nil {
		return 0, err
	}
	return out.Recorded, nil
}

// ListMarks returns the marks recorded against one artifact version.
func (c *Client) ListMarks(workItemID, artifactID string) ([]Annotation, error) {
	var r struct {
		Annotations []Annotation `json:"annotations"`
	}
	path := "/api/v1/work-items/" + url.PathEscape(workItemID) + "/artifacts/" + url.PathEscape(artifactID) + "/annotations"
	if err := c.do("GET", path, nil, &r); err != nil {
		return nil, err
	}
	return r.Annotations, nil
}

// AcceptArtifact marks a version the agreed one (at most one per work item).
func (c *Client) AcceptArtifact(workItemID, artifactID string, accepted bool) error {
	path := "/api/v1/work-items/" + url.PathEscape(workItemID) + "/artifacts/" + url.PathEscape(artifactID)
	return c.do("PATCH", path, map[string]bool{"accepted": accepted}, nil)
}
