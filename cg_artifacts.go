package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// The planning-artifact MCP tools: publish a worked example, then read back what the human drew on it.
//
// This closes the one gap a headless worker cannot close on its own. crank has no browser and never
// sees rendered pixels, so it ships layout work that reads correct and is wrong. Prose does not fix
// that. A mockup the human has marked up does, because the marks come back as acceptance criteria
// that name the element and say what about it is wrong.

var cgPublishArtifactToolDef = map[string]any{
	"name": "publish_artifact",
	"description": "SHOW THE USER WHAT YOU ARE ABOUT TO BUILD, as a self-contained HTML page, before writing any real code. " +
		"Use this whenever the work has a visual or structural shape a sentence cannot pin down — a screen, a layout, a flow, a report, a schema rendered as a table. " +
		"Publish a LOW-FIDELITY page: real structure and real copy, no design polish, no external assets (inline everything; nothing is fetched at render time). " +
		"It returns the exact command for the user to run — hand them that command verbatim, then STOP and wait. " +
		"They will draw on it and their marks come back through read_marks as acceptance criteria you must satisfy. " +
		"Publishing again records a NEW VERSION and carries their unresolved marks forward, so iterate here rather than guessing.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"work_item_id": map[string]any{"type": "string", "description": "the work item this example is for (the id propose_work_item or planning_finalize returned)."},
			"html":         map[string]any{"type": "string", "description": "a complete, self-contained HTML document. Inline all CSS. No external scripts, fonts or stylesheets — they will not load."},
			"title":        map[string]any{"type": "string", "description": "what this version is showing, e.g. 'settings page — two-column'."},
			"note":         map[string]any{"type": "string", "description": "what changed since the previous version, if this is a revision."},
		},
		"required": []string{"work_item_id", "html"},
	},
}

var cgReadMarksToolDef = map[string]any{
	"name": "read_marks",
	"description": "READ WHAT THE USER DREW on a work item's worked example. Call this after they tell you they have finished reviewing. " +
		"Each mark is typed and anchored to an element: scope / behaviour / constraint are requirements you must build to, and question is something ONLY THEY can settle. " +
		"Nothing enforces this for you — you must put the requirements onto the work item as acceptance criteria, and you must ASK the user every question rather than deciding it yourself.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"work_item_id": map[string]any{"type": "string", "description": "the work item whose example was reviewed."},
			"version":      map[string]any{"type": "integer", "description": "optional: a specific version. Defaults to the latest."},
		},
		"required": []string{"work_item_id"},
	},
}

// handleArtifactTool serves publish_artifact and read_marks. Returns false if name is neither, so the
// caller falls through to its own switch.
func (s *cgServer) handleArtifactTool(enc *json.Encoder, reqID json.RawMessage, name string, raw json.RawMessage) bool {
	switch name {
	case "publish_artifact":
		s.publishArtifact(enc, reqID, raw)
	case "read_marks":
		s.readMarks(enc, reqID, raw)
	default:
		return false
	}
	return true
}

func (s *cgServer) publishArtifact(enc *json.Encoder, reqID json.RawMessage, raw json.RawMessage) {
	var p struct {
		Args struct {
			WorkItemID string `json:"work_item_id"`
			HTML       string `json:"html"`
			Title      string `json:"title"`
			Note       string `json:"note"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(raw, &p)
	a := p.Args

	if strings.TrimSpace(a.WorkItemID) == "" {
		s.toolResult(enc, reqID, "publish_artifact needs `work_item_id` — the id propose_work_item or planning_finalize returned.", true)
		return
	}
	if strings.TrimSpace(a.HTML) == "" {
		s.toolResult(enc, reqID, "publish_artifact needs `html` — a complete, self-contained HTML document.", true)
		return
	}

	art, err := api.New().PublishArtifact(a.WorkItemID, a.HTML, a.Title, a.Note)
	if err != nil {
		s.toolResult(enc, reqID, "Could not publish the example: "+err.Error()+
			"\n\nIf that says not found, check the work item id. If the rest of partyline is misbehaving, run `ptln doctor`.", true)
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Published v%d of the worked example.\n\n", art.Version)
	if art.Carried > 0 {
		fmt.Fprintf(&b, "%d unresolved mark(s) from the previous version were carried forward — they are still outstanding.\n\n", art.Carried)
	}
	b.WriteString("GIVE THE USER THIS LINK and wait for them:\n\n")
	fmt.Fprintf(&b, "    %s\n\n", ReviewURL(art.WorkItemID))
	b.WriteString("Hand over the LINK, not a command — it opens in their browser on this machine, where\n")
	b.WriteString("they can draw on the mockup and leave typed marks. If they say nothing loads, their\n")
	fmt.Fprintf(&b, "daemon is not running here; `ptln review %s` does the same thing in one shot.\n\n", art.WorkItemID)
	b.WriteString("When they say they are done, call read_marks to pick up what they left. ")
	b.WriteString("Do NOT start building until you have read their marks.")
	s.toolResult(enc, reqID, b.String(), false)
}

func (s *cgServer) readMarks(enc *json.Encoder, reqID json.RawMessage, raw json.RawMessage) {
	var p struct {
		Args struct {
			WorkItemID string `json:"work_item_id"`
			Version    int    `json:"version"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(raw, &p)
	a := p.Args

	if strings.TrimSpace(a.WorkItemID) == "" {
		s.toolResult(enc, reqID, "read_marks needs `work_item_id`.", true)
		return
	}

	c := api.New()
	arts, err := c.ListArtifacts(a.WorkItemID)
	if err != nil {
		s.toolResult(enc, reqID, "Could not read this work item's examples: "+err.Error(), true)
		return
	}
	if len(arts) == 0 {
		s.toolResult(enc, reqID, "This work item has no worked example yet — publish one with publish_artifact first.", true)
		return
	}

	art := arts[0] // newest first
	if a.Version > 0 {
		found := false
		for _, x := range arts {
			if x.Version == a.Version {
				art, found = x, true
				break
			}
		}
		if !found {
			s.toolResult(enc, reqID, fmt.Sprintf("No version %d — the latest is v%d.", a.Version, arts[0].Version), true)
			return
		}
	}

	marks, err := c.ListMarks(a.WorkItemID, art.ID)
	if err != nil {
		s.toolResult(enc, reqID, "Could not read the marks: "+err.Error(), true)
		return
	}
	s.toolResult(enc, reqID, renderMarks(art, marks), false)
}

// renderMarks writes the marks as the model should act on them: requirements first, then the
// questions that block. The anchor is included because "the third card" is not actionable and
// "section.pricing > article:nth-of-type(3)" is.
func renderMarks(art api.Artifact, marks []api.Annotation) string {
	if len(marks) == 0 {
		return fmt.Sprintf("v%d has no marks on it yet.\n\nIf the user has not reviewed it, give them this link:\n\n    %s\n\nIf they have reviewed it and left nothing, treat the example as agreed.", art.Version, ReviewURL(art.WorkItemID))
	}

	var req, questions []api.Annotation
	for _, m := range marks {
		if m.ResolvedAt != "" {
			continue
		}
		if m.Kind == "question" {
			questions = append(questions, m)
		} else {
			req = append(req, m)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Marks on v%d of the worked example.\n", art.Version)

	if len(req) > 0 {
		b.WriteString("\nREQUIREMENTS — build to these. Each should become an acceptance criterion:\n")
		for i, m := range req {
			fmt.Fprintf(&b, "\n%d. [%s] %s\n", i+1, m.Kind, m.Body)
			writeAnchor(&b, m)
		}
	}

	if len(questions) > 0 {
		b.WriteString("\nOPEN QUESTIONS — ONLY THE USER CAN SETTLE THESE. Ask them; do not decide yourself.\n")
		b.WriteString("Nothing blocks the build on these — that is exactly why you must raise them before starting.\n")
		for i, m := range questions {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, m.Body)
			writeAnchor(&b, m)
		}
	}

	if len(req) == 0 && len(questions) == 0 {
		b.WriteString("\nEvery mark on this version has been resolved.\n")
	}
	return b.String()
}

func writeAnchor(b *strings.Builder, m api.Annotation) {
	if m.Anchor.Selector != "" {
		fmt.Fprintf(b, "   on: %s\n", m.Anchor.Selector)
	}
	if m.Anchor.Text != "" {
		fmt.Fprintf(b, "   text: %q\n", m.Anchor.Text)
	}
	if m.Anchor.Viewport != "" && m.Anchor.Viewport != "desktop" {
		// A complaint made at 390px is about the mobile layout specifically — losing that turns a
		// precise remark into a contradictory one.
		fmt.Fprintf(b, "   at viewport: %s\n", m.Anchor.Viewport)
	}
}
