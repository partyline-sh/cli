package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/surface"
)

// G.2's whole claim is that the four (really five) pause situations are ALREADY distinguished at
// the source and were only being thrown away at the boundary. These pin the mapping so a future
// edit cannot quietly re-collapse them — which is the state this slice exists to leave behind.

// The exit codes are the source of truth crank uses to tell the daemon what happened. If two of
// them ever collide, two unrelated pauses become indistinguishable again.
func TestPauseExitCodesAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, code := range map[string]int{
		"budgetPauseExit": budgetPauseExit,
		"verifyPauseExit": verifyPauseExit,
		"rateLimitExit":   rateLimitExit,
	} {
		if prior, dup := seen[code]; dup {
			t.Errorf("%s and %s share exit code %d — the daemon cannot tell them apart", name, prior, code)
		}
		seen[code] = name
		if code == 0 || code == 1 {
			t.Errorf("%s = %d collides with a plain success/failure exit", name, code)
		}
	}
}

// Every reason the Go side sends must be a declared term, or the database CHECK rejects the write
// and the pause silently keeps its old generic rendering.
func TestEveryReasonWeSendIsDeclared(t *testing.T) {
	for _, r := range []string{
		surface.PauseBudget,
		surface.PauseQuarantine,
		surface.PauseRateLimit,
		surface.PauseEntitlement,
		surface.PauseStall,
	} {
		if !surface.PauseReason.Has(r) {
			t.Errorf("%q is sent by the daemon but not declared — the CHECK constraint will reject it", r)
		}
	}
}

// The distinction that cost real time, asserted rather than left to a comment. An entitlement block
// is NOT a rate limit: a quota reset clears a rate limit, whereas only a human changing billing
// clears an entitlement block. crank.go documents that a run can carry BOTH signals at once and
// that entitlement must win — otherwise the operator watches a countdown to a moment that never
// arrives.
func TestEntitlementIsNotARateLimit(t *testing.T) {
	if surface.PauseEntitlement == surface.PauseRateLimit {
		t.Fatal("entitlement collapsed into rate_limit — waiting cannot clear a billing block")
	}
	// The vocabulary must SAY so, because this is the fact a UI author needs and the one a
	// reasonable person would otherwise get wrong.
	doc := ""
	for _, term := range surface.PauseReason.Terms {
		if term.Key == surface.PauseEntitlement {
			doc = term.Doc
		}
	}
	if doc == "" {
		t.Fatal("entitlement has no documentation")
	}
	for _, want := range []string{"billing", "rate_limit"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the entitlement doc never mentions %q — a UI author will treat it as a rate limit", want)
		}
	}
}

// isEntitlementBlock is what routes a provider refusal to the reason that cannot be waited out.
// These are the shapes crank's comment records as having caused the original misdiagnosis.
func TestEntitlementBlockDetection(t *testing.T) {
	for _, note := range []string{
		"usage credits required",
		"Usage credits required for this org",
		"overage is disabled for your organization",
	} {
		if !isEntitlementBlock(note) {
			t.Errorf("isEntitlementBlock(%q) = false — this pauses as a rate limit and offers a reset that never comes", note)
		}
	}
	for _, note := range []string{
		"rate limit exceeded, resets at 11:30",
		"",
	} {
		if isEntitlementBlock(note) {
			t.Errorf("isEntitlementBlock(%q) = true — a real rate limit would lose its auto-resume", note)
		}
	}
}
