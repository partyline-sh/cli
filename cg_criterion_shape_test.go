package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The exact commands that failed two runs in one afternoon. Both were correctly red on the base
// branch, so the proof gate passed them; both were unsatisfiable in practice because the builder
// could not see the name it had to match.
func TestPinnedShapes_CatchesTheCommandsThatCostARun(t *testing.T) {
	for _, tc := range []struct{ name, cmd, want string }{
		{"go test -run alternation", `go test -run 'PartySender|SenderName|DisplayName' ./... -v 2>&1 | grep -q '^=== RUN'`, "PartySender|SenderName|DisplayName"},
		{"go test -run bare", `go test -run TestCounter ./...`, "TestCounter"},
		{"go test -run equals", `go test -run=TestCounter ./...`, "TestCounter"},
		{"vitest -t", `npx vitest run -t "renders the banner"`, "renders the banner"},
		{"pytest -k", `pytest -k "test_login" -q`, "test_login"},
		{"test -f invented file", `test -f llms_boot_report_test.go`, "llms_boot_report_test.go"},
		{"bracket -f", `[ -f party_identity.go ]`, "party_identity.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pinnedShapes(tc.cmd)
			if len(got) == 0 {
				t.Fatalf("pinnedShapes(%q) found no pin; wanted one naming %q", tc.cmd, tc.want)
			}
			if !strings.Contains(strings.Join(got, " "), tc.want) {
				t.Fatalf("pinnedShapes(%q) = %v; wanted a pin naming %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// A check that runs the whole suite, builds, or exercises a real entry point asserts a RESULT. These
// must stay silent, or the advisory becomes noise and gets learned around — which is how the prose
// version of this rule was ignored.
func TestPinnedShapes_SilentOnOutcomeShapedChecks(t *testing.T) {
	for _, cmd := range []string{
		`go test ./...`,
		`go build ./... && go vet .`,
		`[ -z "$(gofmt -l . | grep -v '^web/')" ] && go build ./... && go test ./...`,
		`cd web && npm run build`,
		`./ptln doctor 2>&1 | grep -q 'party'`,
		`curl -sf localhost:3000/api/v1/health`,
	} {
		if got := pinnedShapes(cmd); len(got) != 0 {
			t.Errorf("pinnedShapes(%q) = %v; wanted no pin — this asserts an outcome", cmd, got)
		}
	}
}

func TestShapeNotice_ReportsPinsAndStaysQuietOtherwise(t *testing.T) {
	pinned := []api.WorkItemCriterion{
		{Text: "the sender carries a display name", Verify: "executable check", Direction: "acceptance",
			Command: `go test -run 'DisplayName' ./... -v 2>&1 | grep -q '^=== RUN'`},
	}
	note := shapeNotice(pinned)
	if note == "" {
		t.Fatal("shapeNotice returned nothing for a criterion that pins a test name")
	}
	if !strings.Contains(note, "DisplayName") {
		t.Errorf("notice does not name the pin it found:\n%s", note)
	}

	clean := []api.WorkItemCriterion{
		{Text: "the suite stays green", Verify: "executable check", Direction: "guard", Command: `go test ./...`},
		{Text: "no PII leaves the process", Verify: "adversarial review"},
	}
	if note := shapeNotice(clean); note != "" {
		t.Errorf("shapeNotice spoke up for outcome-shaped criteria:\n%s", note)
	}
}
