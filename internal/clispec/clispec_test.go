package clispec

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The registry is only worth having if it is COMPLETE. A command that dispatches but has no spec
// gets no help and is invisible to the docs coverage check — which is the failure this epic
// exists to abolish, so it is a test failure rather than a convention.
func TestEveryDispatchedCommandHasASpec(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// The dispatch switch runs from `switch arg {` to the closing brace before the `--` note.
	start := strings.Index(src, "switch arg {")
	end := strings.Index(src, `if arg == "--"`)
	if start < 0 || end < 0 || end < start {
		t.Fatal("could not locate the dispatch switch in main.go — update this matcher")
	}

	var missing []string
	for _, m := range regexp.MustCompile(`(?m)^\s*case ("[a-z_-]+"(?:,\s*"[^"]+")*):`).FindAllStringSubmatch(src[start:end], -1) {
		for _, raw := range strings.Split(m[1], ",") {
			name := strings.Trim(strings.TrimSpace(raw), `"`)
			if _, ok := Lookup(name); !ok {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("dispatched but not declared in Commands: %v", missing)
	}
}

// The reverse direction: a spec for a command nothing dispatches would document a command that
// does not exist — the "phantom" class of docs error, caught here at its source.
func TestEverySpecIsDispatched(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, c := range Commands {
		// llms and help are reachable by paths other than a case label (bare `ptln`, the
		// leading-dash branch), so only assert the name appears in the dispatcher at all.
		if !strings.Contains(src, `"`+c.Name+`"`) {
			t.Errorf("Commands declares %q but main.go never mentions it", c.Name)
		}
	}
}

func TestSpecsAreWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, c := range Commands {
		if c.Name == "" || strings.TrimSpace(c.Summary) == "" {
			t.Errorf("command %q: needs a Name and a Summary", c.Name)
		}
		for _, n := range append([]string{c.Name}, c.Aliases...) {
			if prior, dup := seen[n]; dup {
				t.Errorf("%q is claimed by both %s and %s", n, prior, c.Name)
			}
			seen[n] = c.Name
		}
		for _, f := range c.Flags {
			if strings.HasPrefix(f.Name, "-") {
				t.Errorf("%s: flag %q should be declared without dashes", c.Name, f.Name)
			}
			if strings.TrimSpace(f.Doc) == "" {
				t.Errorf("%s --%s: needs a Doc", c.Name, f.Name)
			}
		}
	}
}

// The bug this slice exists to fix. `ptln work --help` must be recognised as a help request; if
// WantsHelp returns false the dispatcher falls through and work treats "--help" as the task.
func TestWantsHelpCoversTheWorkBug(t *testing.T) {
	work, ok := Lookup("work")
	if !ok {
		t.Fatal("work has no spec")
	}
	for _, argv := range [][]string{
		{"work", "--help"},
		{"work", "-h"},
		{"work", "help"},
	} {
		if !WantsHelp(work, argv) {
			t.Errorf("WantsHelp(%v) = false — this is the bug that started a worker", argv)
		}
	}
	// Not a help request: a real invocation, and a --help buried mid-invocation where the
	// command's own parser should decide.
	for _, argv := range [][]string{
		{"work"},
		{"work", "fix the thing"},
		{"work", "fix the thing", "--help"},
	} {
		if WantsHelp(work, argv) {
			t.Errorf("WantsHelp(%v) = true — that is a real invocation, not a help request", argv)
		}
	}
}

// A command that forwards its arguments to a child must never have --help stolen from it.
func TestPassThroughCommandsAreNotIntercepted(t *testing.T) {
	for _, name := range []string{"new", "llms", "start"} {
		spec, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s has no spec", name)
		}
		if !spec.PassThrough {
			t.Errorf("%s should be PassThrough — it forwards arguments to a child process", name)
		}
		if WantsHelp(spec, []string{name, "--help"}) {
			t.Errorf("ptln %s --help was intercepted; it belongs to the child", name)
		}
	}
}

func TestPrintCmdHelpRendersTheSpec(t *testing.T) {
	var buf bytes.Buffer
	spec, _ := Lookup("crank")
	PrintHelp(&buf, spec)
	out := buf.String()
	for _, want := range []string{"ptln crank", "USAGE", "FLAGS", "--max-tokens N", "Token ceiling"} {
		if !strings.Contains(out, want) {
			t.Errorf("crank help is missing %q\n---\n%s", want, out)
		}
	}
}
