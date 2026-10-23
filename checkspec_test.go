package main

import (
	"strings"
	"testing"
)

// The two things this has to get right: not breaking any existing .partyline/verify file, and not
// letting the control plane's policy reach anything it shouldn't.

func TestParseChecksKeepsUnnamedLinesWorking(t *testing.T) {
	got := parseChecks(`
# the file every project already has
gofmt -l .
go test ./...
`)
	if len(got) != 2 {
		t.Fatalf("got %d checks, want 2 — comments and blanks are not checks", len(got))
	}
	for _, c := range got {
		if c.Named {
			t.Errorf("%q was treated as named; a bare command line is not a named check", c.Cmd)
		}
	}
	if got[0].Cmd != "gofmt -l ." || got[1].Cmd != "go test ./..." {
		t.Errorf("commands were altered: %+v", got)
	}
}

func TestParseChecksReadsNames(t *testing.T) {
	got := parseChecks("build: npm --prefix web run build\ntest:  go test ./...")
	if len(got) != 2 || !got[0].Named || !got[1].Named {
		t.Fatalf("expected two named checks, got %+v", got)
	}
	if got[0].Name != "build" || got[0].Cmd != "npm --prefix web run build" {
		t.Errorf("first check = %+v", got[0])
	}
	if got[1].Cmd != "go test ./..." {
		t.Errorf("trailing whitespace not trimmed from the command: %q", got[1].Cmd)
	}
}

// THE AMBIGUITY THAT MATTERS. Colons are common inside commands. If any colon made a line "named",
// an existing check would silently become a check called "https" or "sh" — and then policy keyed on
// that invented name could switch off something nobody meant to touch.
func TestACommandContainingAColonIsNotAName(t *testing.T) {
	for _, line := range []string{
		`curl -sf https://example.test/health`,
		`sh -c "echo a: b"`,
		`docker run --rm -v /tmp:/tmp alpine true`,
		`Build: npm run build`, // capitalised — not a valid name, so it stays a command
	} {
		got := parseChecks(line)
		if len(got) != 1 {
			t.Fatalf("%q: got %d checks", line, len(got))
		}
		if got[0].Named {
			t.Errorf("%q was read as a NAMED check %q — an existing command would change meaning", line, got[0].Name)
		}
		if got[0].Cmd != line {
			t.Errorf("%q: command was mangled to %q", line, got[0].Cmd)
		}
	}
}

func TestAutoNamesAreUniqueAndReadable(t *testing.T) {
	got := parseChecks("npm --prefix web run build\ngo test ./...\nnpm --prefix web run build")
	names := map[string]bool{}
	for _, c := range got {
		if names[c.Name] {
			t.Errorf("duplicate auto-name %q — the report could not tell two checks apart", c.Name)
		}
		names[c.Name] = true
	}
	if got[0].Name != "build" {
		t.Errorf("auto-name = %q, want the verb (%q) rather than the runner", got[0].Name, "build")
	}
}

// ---- policy resolution ----

func TestNoPolicyMeansTheOldBehaviour(t *testing.T) {
	specs := parseChecks("build: npm run build\ngofmt -l .")
	got := applyPolicy(specs, nil, nil)
	if len(got) != 2 {
		t.Fatalf("got %d checks, want both", len(got))
	}
	for _, c := range got {
		if !c.Blocking || c.Skipped != "" {
			t.Errorf("%q: a check with no policy must run BLOCKING and ALWAYS, got %+v", c.Name, c)
		}
	}
}

// The reason G.4 exists: a check that cannot pass yet can now be listed without rejecting every
// clean diff. partyline's own lint has 38 pre-existing errors.
func TestAnAdvisoryCheckRunsButDoesNotBlock(t *testing.T) {
	specs := parseChecks("build: npm run build\nlint: npm run lint")
	got := applyPolicy(specs, []checkPolicy{
		{Name: "lint", Enabled: true, Blocking: false},
	}, nil)
	var lint resolvedCheck
	for _, c := range got {
		if c.Name == "lint" {
			lint = c
		}
	}
	if lint.Name == "" {
		t.Fatal("the advisory check was dropped — it must still RUN, just not block")
	}
	if lint.Blocking {
		t.Error("lint is marked advisory in policy but resolved as blocking")
	}
}

func TestDisabledChecksDoNotRun(t *testing.T) {
	specs := parseChecks("build: npm run build\nslow: ./slow.sh")
	got := applyPolicy(specs, []checkPolicy{{Name: "slow", Enabled: false}}, nil)
	for _, c := range got {
		if c.Name == "slow" {
			t.Fatal("a check switched off in settings still ran")
		}
	}
}

// THE TRUST RULE. The repo is authoritative about what a check IS. A settings row naming something
// the repo does not have must not conjure it into existence — the control plane holds policy, never
// commands.
func TestPolicyCannotInventACheck(t *testing.T) {
	specs := parseChecks("build: npm run build")
	got := applyPolicy(specs, []checkPolicy{
		{Name: "build", Enabled: true, Blocking: true},
		{Name: "deploy-to-prod", Enabled: true, Blocking: true},
	}, nil)
	if len(got) != 1 || got[0].Name != "build" {
		t.Fatalf("policy conjured a check the repo never declared: %+v", got)
	}
}

// An auto-named check's name moves when its command changes, so honouring policy against it would
// apply an old decision to a different command.
func TestPolicyDoesNotReachAnAutoNamedCheck(t *testing.T) {
	specs := parseChecks("npm run build") // auto-named "build"
	got := applyPolicy(specs, []checkPolicy{{Name: "build", Enabled: false}}, nil)
	if len(got) != 1 {
		t.Fatal("an auto-named check was switched off by policy keyed on a name it did not choose")
	}
}

// ---- path scoping ----

func TestPathGlobSkipsIrrelevantChecks(t *testing.T) {
	specs := parseChecks("webbuild: npm --prefix web run build")
	pol := []checkPolicy{{Name: "webbuild", Enabled: true, Blocking: true, PathGlob: "web/**"}}

	got := applyPolicy(specs, pol, []string{"crank.go", "verify.go"})
	if got[0].Skipped == "" {
		t.Error("a Go-only change still paid for the web build")
	}

	got = applyPolicy(specs, pol, []string{"web/src/app/page.tsx"})
	if got[0].Skipped != "" {
		t.Errorf("a web change skipped the web build: %q", got[0].Skipped)
	}
}

// `web/*` would not match `web/src/app/page.tsx` under filepath.Match, because it does not cross
// separators — and that path is what everyone means by "the web app".
func TestGlobCrossesDirectories(t *testing.T) {
	if !matchGlob("web/**", "web/src/lib/api/runs.ts") {
		t.Error("web/** did not match a nested file — the glob is useless for a real tree")
	}
	if matchGlob("web/**", "crank.go") {
		t.Error("web/** matched a file outside web/")
	}
	if !matchGlob("*.go", "crank.go") {
		t.Error("a plain filepath.Match pattern stopped working")
	}
}

// An unknown change set is not evidence that a check is irrelevant. Running everything is the safe
// default; skipping on no information is how a gate silently stops gating.
func TestUnknownChangeSetRunsEverything(t *testing.T) {
	specs := parseChecks("webbuild: npm --prefix web run build")
	got := applyPolicy(specs, []checkPolicy{
		{Name: "webbuild", Enabled: true, Blocking: true, PathGlob: "web/**"},
	}, nil)
	if got[0].Skipped != "" {
		t.Error("a check was skipped because we did not know what changed")
	}
}

func TestReasonsAreCarriedNotSwallowed(t *testing.T) {
	specs := parseChecks("webbuild: npm run build")
	got := applyPolicy(specs, []checkPolicy{
		{Name: "webbuild", Enabled: true, Blocking: true, PathGlob: "web/**"},
	}, []string{"crank.go"})
	if !strings.Contains(got[0].Skipped, "web/**") {
		t.Errorf("skip reason = %q, want it to name the glob so a human can see WHY", got[0].Skipped)
	}
}
