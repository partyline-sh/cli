package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// renderMarks is what the planning agent actually reads back after a human has drawn on a mockup.
// Its job is to make the two categories impossible to confuse: requirements are things to build,
// questions are things only the human can settle. Blur that and the agent decides a scope question
// on the user's behalf, which is the exact failure planning_open exists to prevent.

func mark(kind, body, selector string) api.Annotation {
	return api.Annotation{Kind: kind, Body: body, Anchor: api.Anchor{Selector: selector}}
}

func TestRenderMarksSeparatesRequirementsFromQuestions(t *testing.T) {
	art := api.Artifact{Version: 3, WorkItemID: "wi-1"}
	out := renderMarks(art, []api.Annotation{
		mark("constraint", "tiers stay on one row above 1024px", "main.tiers"),
		mark("question", "does Fleet show a price?", "main.tiers>article:nth-of-type(3)"),
		mark("behaviour", "clicking a tier opens checkout", "article>button"),
	})

	if !strings.Contains(out, "v3") {
		t.Errorf("version not reported:\n%s", out)
	}
	reqAt := strings.Index(out, "REQUIREMENTS")
	qAt := strings.Index(out, "OPEN QUESTIONS")
	if reqAt < 0 || qAt < 0 {
		t.Fatalf("both sections must appear:\n%s", out)
	}
	if reqAt > qAt {
		t.Errorf("requirements must come before questions:\n%s", out)
	}
	// The question must land under OPEN QUESTIONS, not among the requirements — an agent that reads
	// it as a requirement will answer it itself.
	if strings.Index(out, "does Fleet show a price?") < qAt {
		t.Errorf("a question leaked into the requirements section:\n%s", out)
	}
	// The honest statement, not a claimed gate: nothing enforces these, which is exactly why the
	// agent has to raise them. A test that asserted an enforcement we do not have would lock in a lie.
	if !strings.Contains(out, "Nothing blocks the build") {
		t.Errorf("must say plainly that nothing enforces the questions:\n%s", out)
	}
	if strings.Contains(out, "planning_finalize will refuse") {
		t.Errorf("claims an enforcement that does not exist:\n%s", out)
	}
	if !strings.Contains(out, "main.tiers") {
		t.Errorf("the anchor must be included — 'the third card' is not actionable:\n%s", out)
	}
}

func TestRenderMarksIgnoresResolved(t *testing.T) {
	m := mark("constraint", "already handled", "main")
	m.ResolvedAt = "2026-08-18T00:00:00Z"
	out := renderMarks(api.Artifact{Version: 1}, []api.Annotation{m})
	if strings.Contains(out, "already handled") {
		t.Errorf("a resolved mark must not be re-raised:\n%s", out)
	}
	if !strings.Contains(out, "resolved") {
		t.Errorf("expected a note that everything is resolved:\n%s", out)
	}
}

func TestRenderMarksWithNoneTellsTheAgentWhatToDo(t *testing.T) {
	out := renderMarks(api.Artifact{Version: 2, WorkItemID: "wi-9"}, nil)
	// A bare "no marks" is ambiguous between "not reviewed yet" and "reviewed, nothing to say", and
	// those call for opposite next moves. The message must name both and carry something the user
	// can act on — a LINK, because nobody types `ptln review`: the agent hands over a URL that the
	// daemon is already serving.
	if !strings.Contains(out, "/w/wi-9") {
		t.Errorf("expected the review link:\n%s", out)
	}
	if !strings.Contains(out, "agreed") {
		t.Errorf("expected the 'reviewed and left nothing' reading:\n%s", out)
	}
}

func TestRenderMarksKeepsNonDesktopViewport(t *testing.T) {
	m := mark("constraint", "cards must stack", "main.tiers")
	m.Anchor.Viewport = "mobile"
	out := renderMarks(api.Artifact{Version: 1}, []api.Annotation{m})
	// A complaint made at 390px is about the mobile layout specifically. Dropping the viewport turns
	// "stack these" into a contradiction of the desktop requirement sitting next to it.
	if !strings.Contains(out, "mobile") {
		t.Errorf("viewport must survive into the agent-facing text:\n%s", out)
	}

	d := mark("constraint", "one row", "main.tiers")
	d.Anchor.Viewport = "desktop"
	if strings.Contains(renderMarks(api.Artifact{Version: 1}, []api.Annotation{d}), "at viewport") {
		t.Error("desktop is the default and should not be annotated as a special case")
	}
}
