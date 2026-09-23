package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Filing work through the readiness gate (#840, epic #836).
//
// The gate lives on the SERVER (web/src/lib/api/readiness.ts) — deliberately, because a check that
// runs in the client is a check the client can skip. This is only the wire shape.

// HeldBack is the server's "not yet, ask the human this" answer.
//
// It arrives as HTTP 200. That is not an oversight: an MCP client surfaces a non-2xx as a TOOL
// FAILURE, and a model that sees a failure apologises, retries the same content, or reports that it
// could not file the item — every branch except putting the questions to the human. The 200 is what
// makes this a conversation rather than an error.
type HeldBack struct {
	Filed         bool          `json:"filed"`
	Readiness     int           `json:"readiness"`
	ReadinessNote string        `json:"readiness_note"`
	Missing       []MissingItem `json:"missing"`
	Guidance      string        `json:"guidance"`
	// Tasks is populated on the TREE path: which leaves are not ready, so a decomposition reports
	// every problem at once rather than one per round trip.
	Tasks []HeldBackTask `json:"tasks,omitempty"`
}

type MissingItem struct {
	Dimension   string `json:"dimension"`
	AskTheHuman string `json:"ask_the_human"`
}

type HeldBackTask struct {
	Title     string        `json:"title"`
	Readiness int           `json:"readiness"`
	Missing   []MissingItem `json:"missing"`
}

// Text renders a held-back answer as the instruction the model acts on. Written as prose rather than
// returned as JSON because this goes into a language model's context, and a model handed a JSON blob
// tends to summarise it — losing exactly the verbatim wording that makes the questions answerable.
func (h *HeldBack) Text(what string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Not filed yet — %s needs a bit more before an agent can build it.\n\n", what)
	if len(h.Tasks) > 0 {
		for _, t := range h.Tasks {
			fmt.Fprintf(&b, "TASK: %s\n", t.Title)
			for _, m := range t.Missing {
				fmt.Fprintf(&b, "  - %s\n", m.AskTheHuman)
			}
			b.WriteString("\n")
		}
	} else {
		for _, m := range h.Missing {
			fmt.Fprintf(&b, "  - %s\n", m.AskTheHuman)
		}
		b.WriteString("\n")
	}
	b.WriteString("Ask the human these questions AS WRITTEN, then call this tool again with their answers.\n")
	b.WriteString("Do not invent answers, and do not pad the description to raise the score — the checks are ")
	b.WriteString("structural (does the project exist, is there a command that verifies it), so more prose changes nothing.")
	return b.String()
}

// SendWorkItem files ONE item through the gate.
//
// Returns (id, nil, nil) when filed, ("", held, nil) when the server wants more from the human, and
// an error only for a genuine failure. Three-way rather than an error for "held back" precisely so a
// caller cannot accidentally treat a normal conversational outcome as a fault.
func (c *Client) SendWorkItem(threadID, kind, title, parentID, document, projectLabel string, criteria []WorkItemCriterion, preview bool) (string, *HeldBack, error) {
	in := map[string]any{
		"thread_id": threadID, "kind": kind, "title": title,
		"project_label": projectLabel, "derive": true,
	}
	if parentID != "" {
		in["parent_id"] = parentID
	}
	if document != "" {
		in["document"] = document
	}
	if len(criteria) > 0 {
		in["acceptance_criteria"] = criteria
	}
	if preview {
		in["preview"] = true
	}

	var raw json.RawMessage
	if err := c.do("POST", "/api/v1/work-items", in, &raw); err != nil {
		return "", nil, err
	}
	var out struct {
		ID    string `json:"id"`
		Filed *bool  `json:"filed"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Filed != nil && !*out.Filed {
		var h HeldBack
		_ = json.Unmarshal(raw, &h)
		return "", &h, nil
	}
	return out.ID, nil, nil
}

// SendWorkTree files a whole decomposition through the gate, ALL-OR-NOTHING: one unready leaf holds
// the entire tree, and every offending leaf comes back at once. A half-filed plan looks complete on
// the board and only reveals its holes at Start.
func (c *Client) SendWorkTree(threadID, projectLabel string, root WorkTreeNode, preview bool) (string, int, *HeldBack, error) {
	in := map[string]any{"thread_id": threadID, "root": root, "project_label": projectLabel, "derive": true}
	if preview {
		in["preview"] = true
	}
	var raw json.RawMessage
	if err := c.do("POST", "/api/v1/work-items/tree", in, &raw); err != nil {
		return "", 0, nil, err
	}
	var out struct {
		RootID string `json:"root_id"`
		Count  int    `json:"count"`
		Filed  *bool  `json:"filed"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Filed != nil && !*out.Filed {
		var h HeldBack
		_ = json.Unmarshal(raw, &h)
		return "", 0, &h, nil
	}
	return out.RootID, out.Count, nil, nil
}
