package main

import "testing"

// TestTaskHasAcceptanceCue is table-driven: tasks that reference a verifiable check (one per cue
// keyword, case-insensitively) → true; vague/fuzzy tasks with no check → false. Pins the #76 heuristic.
func TestTaskHasAcceptanceCue(t *testing.T) {
	cases := []struct {
		name string
		task string
		want bool
	}{
		// with a cue → true (one per keyword)
		{"go test", "add coverage — acceptance: go test . -run TestFoo passes", true},
		{"npx tsc", "change the config, then run cd web && npx tsc --noEmit", true},
		{"tsc --noEmit standalone", "verify with tsc --noEmit that no new type errors appear", true},
		{"gofmt", "reformat helpers.go so gofmt -l prints nothing", true},
		{"must pass", "the new subtests must pass before merge", true},
		{"acceptance: label", "acceptance: the build is green", true},
		{"verify: label", "verify: the endpoint returns 200", true},
		{"passes", "ensure the suite passes after the edit", true},
		{"assert", "assert the result equals the expected slice", true},
		{"case-insensitive", "Acceptance: GO TEST ./... PASSES", true},
		// no cue → false
		{"fuzzy posthog (round-1 anti-example)", "disable dead-click autocapture via the PostHog config options", false},
		{"make it work", "make the login flow work again", false},
		{"empty", "", false},
		{"vague", "improve the docs a bit", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskHasAcceptanceCue(tc.task); got != tc.want {
				t.Errorf("taskHasAcceptanceCue(%q) = %v, want %v", tc.task, got, tc.want)
			}
		})
	}
}
