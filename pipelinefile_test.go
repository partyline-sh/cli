package main

import (
	"os"
	"path/filepath"
	"testing"
)

// This file is where a control-plane value lands on the machine that executes things. Everything
// below is a variation on one question: can anything in that file change what RUNS?

func writePipeline(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pipeline.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPipelineFileRoundTrips(t *testing.T) {
	checks, lanes := readPipelineFile(writePipeline(t, `{
	  "checks": [{"name":"lint","enabled":true,"blocking":false,"path_glob":"web/**"}],
	  "lanes":  [{"id":"primary","engine":"claude"},{"id":"second","engine":"codex","model":"gpt-5"}]
	}`))

	if len(checks) != 1 || checks[0].Name != "lint" || checks[0].Blocking || checks[0].PathGlob != "web/**" {
		t.Fatalf("checks = %+v", checks)
	}
	if len(lanes) != 2 || lanes[1].Engine != "codex" || lanes[1].Model != "gpt-5" {
		t.Fatalf("lanes = %+v", lanes)
	}
}

// FAIL-SOFT, IN THE STRICT DIRECTION. A file we cannot read must yield the DEFAULT pipeline — every
// check blocking, one reviewer — not a permissive one. Verified through applyPolicy, because "nil
// policy" only counts as safe if what it resolves to is the old behaviour.
func TestAnUnreadablePipelineFallsBackToTheStrictDefault(t *testing.T) {
	for _, body := range []string{"", "not json at all", `{"checks": "a string"}`, `[]`} {
		checks, lanes := readPipelineFile(writePipeline(t, body))
		if checks != nil || lanes != nil {
			t.Errorf("%q yielded policy %+v / %+v, want none", body, checks, lanes)
		}
	}
	checks, lanes := readPipelineFile(filepath.Join(t.TempDir(), "absent.json"))
	if checks != nil || lanes != nil {
		t.Error("a missing file produced policy")
	}

	resolved := applyPolicy(parseChecks("build: npm run build\nlint: npm run lint"), nil, nil)
	if len(resolved) != 2 {
		t.Fatalf("the default resolution dropped a check: %+v", resolved)
	}
	for _, c := range resolved {
		if !c.Blocking || c.Skipped != "" {
			t.Errorf("%q resolved to %+v — no policy must mean blocking and always-run", c.Name, c)
		}
	}
}

// THE BOUNDARY. A lane names an engine from the closed set. A file naming something else — a path, a
// shell command, an engine this machine doesn't have — must not reach an argv, and eng.Valid is the
// re-check that ensures a server value is never trusted on the machine that will run it.
func TestALaneCannotNameSomethingThatIsNotAnEngine(t *testing.T) {
	for _, engine := range []string{
		"/bin/sh",
		"claude; rm -rf /",
		"../../usr/bin/env",
		"definitely-not-an-engine",
		"",
	} {
		_, lanes := readPipelineFile(writePipeline(t,
			`{"lanes":[{"id":"x","engine":`+quoteJSON(engine)+`}]}`))
		if len(lanes) != 0 {
			t.Errorf("engine %q was accepted as a lane: %+v", engine, lanes)
		}
	}
}

// A model token heads straight for an exec argv, so it gets the same shape gate as every other one.
func TestALaneModelIsShapeGated(t *testing.T) {
	for _, model := range []string{"--dangerously-skip-permissions", "a b", "/etc/passwd", "-flag"} {
		_, lanes := readPipelineFile(writePipeline(t,
			`{"lanes":[{"id":"x","engine":"claude","model":`+quoteJSON(model)+`}]}`))
		if len(lanes) != 0 {
			t.Errorf("model %q was accepted: %+v", model, lanes)
		}
	}
	if _, lanes := readPipelineFile(writePipeline(t, `{"lanes":[{"id":"x","engine":"claude"}]}`)); len(lanes) != 1 || lanes[0].Model != "" {
		t.Error("an empty model must be allowed — it means the engine's own default")
	}
}

// A check is addressed by name, and a name that could never match a repo-declared check is not a
// check. Keeping one would just be a way to carry a long string into memory.
func TestAMalformedCheckNameIsDropped(t *testing.T) {
	_, _ = readPipelineFile(writePipeline(t, `{}`))
	checks, _ := readPipelineFile(writePipeline(t, `{"checks":[
	  {"name":"Build","enabled":true},
	  {"name":"rm -rf /","enabled":true},
	  {"name":"","enabled":true},
	  {"name":"ok-one","enabled":true,"blocking":true}
	]}`))
	if len(checks) != 1 || checks[0].Name != "ok-one" {
		t.Fatalf("checks = %+v, want only the well-formed one", checks)
	}
}

// Bounded at the source. Lanes especially: each is a full reviewer pass, so an unbounded list is a
// token bill, not just a big slice.
func TestPipelinePolicyIsBounded(t *testing.T) {
	var lanes string
	for i := 0; i < 40; i++ {
		if i > 0 {
			lanes += ","
		}
		lanes += `{"id":"lane` + itoaCheck(i) + `","engine":"claude"}`
	}
	_, got := readPipelineFile(writePipeline(t, `{"lanes":[`+lanes+`]}`))
	if len(got) > maxPolicyLanes {
		t.Errorf("got %d lanes, want at most %d", len(got), maxPolicyLanes)
	}

	big := make([]byte, maxPipelineBytes+1)
	for i := range big {
		big[i] = ' '
	}
	if c, l := readPipelineFile(writePipeline(t, string(big))); c != nil || l != nil {
		t.Error("an oversized file was parsed")
	}
}

// Duplicate ids would make the report's per-lane attribution meaningless — two rows both labelled
// "primary" cannot be told apart when they disagree.
func TestDuplicateIdsCollapse(t *testing.T) {
	_, lanes := readPipelineFile(writePipeline(t,
		`{"lanes":[{"id":"a","engine":"claude"},{"id":"a","engine":"codex"}]}`))
	if len(lanes) != 1 || lanes[0].Engine != "claude" {
		t.Errorf("lanes = %+v, want the first only", lanes)
	}
}

func quoteJSON(s string) string {
	out := `"`
	for _, r := range s {
		switch r {
		case '"':
			out += `\"`
		case '\\':
			out += `\\`
		default:
			out += string(r)
		}
	}
	return out + `"`
}
