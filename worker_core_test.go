package main

import (
	"strings"
	"testing"
)

// THE CORE EXISTS TO STOP TWO THINGS: rules missing from a persona that needs them, and twins
// drifting apart. Both had already happened before it was extracted.

// workerPrompt and workerResumePrompt are the same agent in two situations. Every standing rule must
// reach both — the resume variant had silently lost the stop-run branch and half the headless rule,
// because they were hand-copied.
func TestBothBuilderPersonasCarryEveryStandingRule(t *testing.T) {
	fresh := workerPrompt("do the thing", "", false, true, false)
	resumed := workerResumePrompt("do the thing", false, true)

	for _, rule := range []struct{ tag, why string }{
		{"<grounding>", "never claim what you did not open"},
		{"<conventions>", "match the codebase you are in"},
		{"<scope>", "do what was asked and stop"},
		{"<solve_it_properly>", "do not hard-code to satisfy a check"},
		{"<reversibility>", "no destructive shortcuts"},
		{"<uncertainty>", "permission to say I don't know"},
		{"<no_eyes>", "cannot see what it produced"},
		{"<summary_format>", "the shape the reviewer reads"},
	} {
		if !strings.Contains(fresh, rule.tag) {
			t.Errorf("workerPrompt is missing %s (%s)", rule.tag, rule.why)
		}
		if !strings.Contains(resumed, rule.tag) {
			t.Errorf("workerResumePrompt is missing %s (%s) — the twins have drifted again", rule.tag, rule.why)
		}
	}
}

// The hard-coding rule is load-bearing for any run whose definition of done is an executable check.
// An agent optimising for "make the check pass" special-cases the check; the whole verify gate rests
// on it not doing that.
func TestTheBuilderIsToldNotToGameTheCheck(t *testing.T) {
	p := workerPrompt("make the failing test pass", "", false, true, false)
	for _, phrase := range []string{"not one shaped to satisfy a check", "Do not hard-code", "SAY SO instead of working around it"} {
		if !strings.Contains(p, phrase) {
			t.Errorf("the builder is no longer told %q — a run judged by an executable check will be gamed", phrase)
		}
	}
}

// THE PROMPTS RUN ON WHATEVER THE USER IS BUILDING. A rule that names a language, a package manager
// or a test runner is wrong for every user who does not use it — and these prompts ship to all of
// them. The old posture told every worker to run `npm test` / `npx tsc --noEmit` / `go test ./...`,
// which is noise in a Rust service and misleading in a Django app.
func TestTheSharedRulesAssumeNoLanguageOrToolchain(t *testing.T) {
	shared := workerCore() + coreHeadless + coreSummary
	for _, leak := range []string{
		"npm", "npx", "yarn", "pnpm", "tsc", "vitest", "jest", "package.json",
		"go test", "cargo", "pytest", "gradle", "Makefile", "auto-rows-fr", "CSS", "browser",
	} {
		if strings.Contains(shared, leak) {
			t.Errorf("the shared core names %q — it must hold for any stack a user builds on", leak)
		}
	}
}

// The bash posture necessarily talks about running checks, so it may not name a toolchain either —
// it must tell the worker to FIND this project's commands instead of assuming a default.
func TestTheVerifyInstructionSendsTheWorkerLooking(t *testing.T) {
	p := workerBashPosture(true)
	for _, leak := range []string{"npm test", "npx tsc", "go test ./...", "package.json"} {
		if strings.Contains(p, leak) {
			t.Errorf("the verify instruction still names %q — wrong for most projects it will run in", leak)
		}
	}
	if !strings.Contains(p, "FIND the commands") {
		t.Error("the verify instruction no longer tells the worker to find this project's own commands")
	}
}

// An example steers output shape far more reliably than describing it, and the summary is what the
// reviewer, the board card and the human all read.
func TestTheSummaryFormatShowsAnExampleRatherThanDescribingOne(t *testing.T) {
	if !strings.Contains(coreSummary, "<example>") {
		t.Error("the summary format no longer carries a worked example")
	}
	if !strings.Contains(coreSummary, "Not verified:") {
		t.Error("the example no longer demonstrates stating what was NOT verified — the part most often skipped")
	}
}
