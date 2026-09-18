package main

import (
	"os"
	"strings"
	"testing"
)

// S.2 — the emergent half. Everything downstream of a proposal already existed: the states, the
// approve/decline commands, the rule that a proposed skill is invisible to every agent, and
// injection of approved ones into every run. The missing piece was a door for the agent that
// NOTICED the recurring problem, and party_mcp.go called it out as a fast-follow in a comment.
func TestAnAgentCanProposeASkill(t *testing.T) {
	src, err := os.ReadFile("party_mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, `"name": "skill_propose"`) {
		t.Fatal("skill_propose is not advertised — an agent that spots a recurring problem still has no way to say so")
	}
	if !strings.Contains(body, `case "skill_propose":`) {
		t.Error("skill_propose is advertised but not dispatched")
	}
	// The old comment promising this as a fast-follow should be gone, or it reads as still-missing.
	if strings.Contains(body, "skill_propose → human approval, mirroring plan_propose) are a documented fast-follow") {
		t.Error("the fast-follow note survives the thing it was promising")
	}
}

// THE APPROVAL BOUNDARY IS AUTHORSHIP, NOT CREDENTIAL. An admin pushing by hand is the approver, so
// asking them to approve their own push is theatre — that rule is right for a PERSON. It is wrong
// for an agent holding that admin's token: the agent did not decide, and letting it inherit the
// human's authority is exactly how "a human accepts before any agent sees it" stops being true.
func TestAnAgentProposalIsNeverAutoApproved(t *testing.T) {
	src, err := os.ReadFile("web/src/lib/api/skills.ts")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "proposedBy") {
		t.Fatal("pushSkill has no way to mark an agent proposal — an admin's agent would publish live skills")
	}
	// THE INVARIANT, stated as the thing that must never appear: pushStatusFor is the role check,
	// and every call to it must sit in the FALSE branch of a proposedBy ternary. An UNGUARDED call
	// means some write path decides status by role, which for an agent proposal means publishing
	// live under whatever authority the token happened to carry.
	//
	// Counting guards was not enough — pushSkill has two write paths and four mentions, so deleting
	// one guard still cleared a threshold of two. This asserts the absence of the broken shape
	// instead, which cannot be satisfied by an unrelated occurrence elsewhere.
	for _, unguarded := range []string{
		"const st = await pushStatusFor(",
		"...(await pushStatusFor(",
	} {
		if strings.Contains(body, unguarded) {
			t.Errorf("an UNGUARDED role check survives (%q) — a proposal down that path publishes live", unguarded)
		}
	}
	// And the forced value must actually be `proposed` with no approver recorded.
	if !strings.Contains(body, `status: "proposed" as const, approved_by: null, approved_at: null`) {
		t.Error("an agent proposal does not force proposed-with-no-approver")
	}
}

// A proposal with no evidence hands the reviewer the work it was supposed to save them: deciding
// whether this is a pattern or a one-off. The tool refuses rather than filing a blank case.
func TestAProposalMustCarryItsEvidence(t *testing.T) {
	src, _ := os.ReadFile("party_mcp.go")
	body := string(src)
	h := body[strings.Index(body, `case "skill_propose":`):]
	if i := strings.Index(h, `case "skill_list":`); i > 0 {
		h = h[:i]
	}
	if !strings.Contains(h, "a.Reason") || !strings.Contains(h, "needs a `reason`") {
		t.Error("skill_propose accepts a proposal with no reason — the reviewer gets no case to judge")
	}
	// And the schema must demand it, not merely mention it.
	def := body[strings.Index(body, `"name": "skill_propose"`):]
	if i := strings.Index(def, "},\n\t{"); i > 0 {
		def = def[:i]
	}
	if !strings.Contains(def, `"reason"`) || !strings.Contains(def, `[]string{"name", "description", "body", "reason"}`) {
		t.Error("`reason` is not a required field on skill_propose")
	}
}
