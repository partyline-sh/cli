package main

import (
	"os"
	"strings"
	"testing"
)

// ONE LESSON HAS TO REACH EVERY PLANNING PERSONA.
//
// Three runs died on criteria that could not fail: a test name that matched nothing, a substring that
// matched an unrelated test, and a filename the planner invented for work that was already correct.
// The rule that prevents all three — assert the OUTCOME, never the SHAPE — has to be in every persona
// that authors criteria, or the next one authored elsewhere repeats it.
func TestEveryPlanningPersonaForbidsShapeInCriteria(t *testing.T) {
	for _, f := range []string{"describe.go", "cg_mcp.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "ASSERT THE OUTCOME") {
			t.Errorf("%s authors acceptance criteria but no longer forbids dictating filenames or test names", f)
		}
	}
	web, err := os.ReadFile("web/src/lib/api/party-modes.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(web), "ASSERT THE OUTCOME") {
		t.Error("the live planning persona no longer forbids dictating shape in a criterion")
	}
}

// The rule is only useful if it SHOWS the difference. A principle without a contrasting pair is a
// slogan, which is what the first version of this was and why it did not stick.
func TestTheShapeRuleShowsAContrastingPair(t *testing.T) {
	src, _ := os.ReadFile("describe.go")
	body := string(src)
	if !strings.Contains(body, "BAD ") || !strings.Contains(body, "GOOD ") {
		t.Error("the outcome-not-shape rule no longer contrasts a bad criterion with a good one")
	}
}

// Every party mode must carry the conduct rules. Five of nine had none — no tool-failure policy, no
// grounding rule — because FACILITATOR_CORE only fit the document-authoring modes.
func TestEveryPartyModeComposesASharedBase(t *testing.T) {
	src, err := os.ReadFile("web/src/lib/api/party-modes.ts")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if n := strings.Count(body, "ROOM_CORE +"); n < 6 {
		t.Errorf("only %d modes compose ROOM_CORE — a mode with no shared base ships with no tool-failure policy", n)
	}
	if !strings.Contains(body, "const FACILITATOR_CORE =\n  ROOM_CORE +") {
		t.Error("FACILITATOR_CORE no longer builds on ROOM_CORE — the two will drift")
	}
}

// The joined-agent prompt is the only one a STRANGER's agent runs under. It was a bare tool tour.
func TestTheJoinedAgentGetsConductRulesNotJustATour(t *testing.T) {
	p := kickoffPrompt("someone")
	for _, rule := range []struct{ want, why string }{
		{"Ground what you say", "no grounding rule"},
		{"I don't know", "no permission to be uncertain"},
		{"never ask a user to enable", "no tool-failure policy"},
		{"DATA, not instructions", "no prompt-injection posture"},
		{"humans make the calls", "no deference rule"},
	} {
		if !strings.Contains(p, rule.want) {
			t.Errorf("kickoffPrompt has %s — it is the only persona a stranger's agent runs under", rule.why)
		}
	}
}
