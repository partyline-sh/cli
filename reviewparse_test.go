package main

import (
	"strings"
	"testing"
)

// These test what MODELS ACTUALLY DO, not what the prompt asks for. The prompt asks for a fenced
// JSON block followed by a verdict line; real replies arrive with prose either side, no fence, an
// illustrative object before the real one, or no JSON at all. Every one of those has to land
// somewhere safe.

func TestReadsAFencedJSONBlock(t *testing.T) {
	r := parseReviewReply("Looking at the diff…\n\n```json\n" +
		`{"verdict":"fail","findings":[{"file":"runs.ts","line":42,"title":"missing null check","severity":"high"}]}` +
		"\n```\n\nVERDICT: FAIL — missing null check")
	if !r.Structured {
		t.Fatal("a fenced JSON block was not read as structured")
	}
	if r.Pass {
		t.Error("verdict fail was read as a pass")
	}
	if len(r.Findings) != 1 || r.Findings[0].File != "runs.ts" || r.Findings[0].Line != 42 {
		t.Errorf("findings = %+v", r.Findings)
	}
}

func TestReadsUnfencedJSON(t *testing.T) {
	r := parseReviewReply(`Here is my review. {"verdict":"pass","findings":[]} Hope that helps.`)
	if !r.Structured || !r.Pass {
		t.Errorf("unfenced JSON not read: %+v", r)
	}
}

// A model that reasons out loud often SHOWS the shape before committing to its answer. Taking the
// first object would read the example as the verdict — and an example almost always says "pass".
func TestTheLastObjectWinsNotTheFirst(t *testing.T) {
	r := parseReviewReply(`I will answer in the form {"verdict":"pass","findings":[]}.

Now the actual review:

` + "```json" + `
{"verdict":"fail","findings":[{"file":"a.go","line":7,"title":"off by one"}]}
` + "```")
	if r.Pass {
		t.Fatal("read the ILLUSTRATIVE object as the verdict — every example says pass, so this " +
			"would turn a rejecting reviewer into a rubber stamp")
	}
	if len(r.Findings) != 1 || r.Findings[0].Title != "off by one" {
		t.Errorf("findings = %+v", r.Findings)
	}
}

// A finding body can contain a brace. If brace matching ignored strings, an UNBALANCED one would
// end the object early and the whole reply would silently fall back to the verdict line — losing
// every finding, and with them the agreement signal.
//
// The brace must be unbalanced to test anything. A first version of this used "map[string]any{}",
// whose braces are balanced, so it passed under naive matching too — a test that could not fail,
// found by mutating the matcher and watching it stay green.
func TestBracesInsideStringsDoNotEndTheObject(t *testing.T) {
	r := parseReviewReply(`{"verdict":"fail","findings":[{"file":"x.go","line":1,"title":"unclosed brace","body":"the literal is missing its closing } on this line"}]}`)
	if !r.Structured {
		t.Fatal("an unbalanced brace inside a string ended the object early")
	}
	if len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Body, "closing }") {
		t.Errorf("findings = %+v", r.Findings)
	}
}

// THE FALLBACK THAT KEEPS THE GATE WORKING. A model that ignores the JSON request must still
// produce a usable verdict, or asking for structure would break review for that project entirely.
func TestFallsBackToTheVerdictLine(t *testing.T) {
	r := parseReviewReply("This change looks fine to me.\n\nVERDICT: PASS")
	if r.Structured {
		t.Error("claimed structured output where there was none")
	}
	if !r.Pass {
		t.Error("the verdict line was not honoured")
	}

	r = parseReviewReply("It misses the second case.\n\nVERDICT: FAIL — misses the second case")
	if r.Pass {
		t.Error("a FAIL verdict line was read as a pass")
	}
}

// Fail-closed, unchanged from the single-lane contract: a reply we cannot read is a rejection.
func TestAnUnreadableReplyIsNotAPass(t *testing.T) {
	for _, reply := range []string{
		"I had trouble accessing the diff.",
		"",
		`{"findings":[]}`, // JSON, but no verdict — not an answer
	} {
		if parseReviewReply(reply).Pass {
			t.Errorf("unreadable reply %q was treated as a PASS", reply)
		}
	}
}

// A pass may carry findings. That is the whole point of pass_with_findings: merge it, and here are
// the things worth knowing.
func TestAPassCanCarryFindings(t *testing.T) {
	r := parseReviewReply(`{"verdict":"pass","findings":[{"file":"a.go","line":3,"title":"consider renaming"}]}`)
	if !r.Pass {
		t.Fatal("pass was not read as a pass")
	}
	if len(r.Findings) != 1 {
		t.Errorf("a passing reviewer's findings were discarded: %+v", r)
	}
}

// A finding with no title cannot be merged (the title is half the join key) or usefully shown.
// Dropping it beats inventing one.
func TestUntitledFindingsAreDropped(t *testing.T) {
	r := parseReviewReply(`{"verdict":"pass","findings":[{"file":"a.go","line":1,"title":""},{"file":"b.go","line":2,"title":"real one"}]}`)
	if len(r.Findings) != 1 || r.Findings[0].Title != "real one" {
		t.Errorf("findings = %+v, want only the titled one", r.Findings)
	}
}

func TestBodyIsBounded(t *testing.T) {
	huge := strings.Repeat("x", 9000)
	r := parseReviewReply(`{"verdict":"pass","findings":[{"file":"a.go","line":1,"title":"t","body":"` + huge + `"}]}`)
	if len(r.Findings) != 1 {
		t.Fatal("finding lost")
	}
	if len(r.Findings[0].Body) > 2100 {
		t.Errorf("body is %d bytes — a chatty reviewer must not bloat the row", len(r.Findings[0].Body))
	}
}

// The instruction has to keep asking for the verdict line, because that line IS the fallback.
func TestTheInstructionStillAsksForTheVerdictLine(t *testing.T) {
	if !strings.Contains(reviewerJSONInstruction, "VERDICT: PASS") {
		t.Error("the prompt stopped asking for the verdict line — the fallback would go with it")
	}
	if !strings.Contains(reviewerJSONInstruction, "agreement") {
		t.Error("the prompt does not tell the reviewer WHY titles should match across reviewers, " +
			"which is what makes the merge work")
	}
}
