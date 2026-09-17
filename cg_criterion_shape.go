package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// cg_criterion_shape.go — name the criteria that pin a SHAPE, at the moment they are authored.
//
// THE HOLE THIS FILLS. proveCriteria runs every command before filing and refuses the ones that
// cannot fail. That catches a decorative check. It cannot catch the opposite defect, because the two
// are indistinguishable by exit code:
//
//	go test -run TestDisplayName ./... | grep -q '=== RUN'   → red today, because the test is unwritten
//	go test -run TestNothingEver ./... | grep -q '=== RUN'   → red today, and red FOREVER
//
// Both are correctly red on the base branch, so both sail through the proof gate. The difference only
// shows up 15 minutes later, in a worker that named its test something else and failed a check it was
// never shown. That happened twice in one afternoon — two runs, both with complete and correct work,
// both failed on the spelling of a test name.
//
// Disclosing the command to the builder (workItemTaskText) is what makes a pinned name SATISFIABLE.
// This is the other half: telling the AUTHOR, at authoring time, that they have written a check the
// work must be shaped to rather than one the work must be true to. Pinning is legitimate — "put it
// here, call it this" is a fair thing for a spec to say — so this INFORMS, it does not refuse. A
// refusal here would be the tool overruling a deliberate architectural instruction, which is not its
// call to make.
//
// Deliberately narrow: only the forms that have actually cost a run. A heuristic that flags half the
// commands in the repo would be read as noise and learned around, which is how the prose version of
// this rule (already in the planning tool's description) got ignored — including by its author.

var shapePinPatterns = []struct {
	what string
	re   *regexp.Regexp
}{
	// `go test -run <pat>` / `-run=<pat>` — the form that killed both runs.
	{"a test-name filter", regexp.MustCompile(`\bgo\s+test\b[^|;&]*?\s-run[=\s]+('[^']*'|"[^"]*"|[^\s|;&]+)`)},
	// vitest / jest name filters.
	{"a test-name filter", regexp.MustCompile(`\b(?:vitest|jest)\b[^|;&]*?\s(?:-t|--testNamePattern)[=\s]+('[^']*'|"[^"]*"|[^\s|;&]+)`)},
	// pytest keyword filter.
	{"a test-name filter", regexp.MustCompile(`\bpytest\b[^|;&]*?\s-k[=\s]+('[^']*'|"[^"]*"|[^\s|;&]+)`)},
	// `test -f <path>` / `[ -f <path> ]` — asserting a file EXISTS is asserting where the work lives.
	{"a specific file path", regexp.MustCompile(`(?:\btest\s+-[ef]|\[\s+-[ef])\s+('[^']*'|"[^"]*"|[^\s|;&\]]+)`)},
}

// pinnedShapes reports the shape-pinning operands in one command, tagged with what kind of pin each
// is. Returns nil for a command that asserts behaviour rather than layout.
func pinnedShapes(command string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range shapePinPatterns {
		for _, m := range p.re.FindAllStringSubmatch(command, -1) {
			v := unquoteOperand(m[1])
			if v == "" {
				continue
			}
			key := p.what + "\x00" + v
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fmt.Sprintf("%s (%s)", v, p.what))
		}
	}
	sort.Strings(out)
	return out
}

func unquoteOperand(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			s = s[1 : len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

// shapeNotice renders the advisory appended to a successful finalize, or "" when no criterion pins a
// shape. It reports rather than refuses: the pin is now visible to the builder, so it is workable —
// the author just needs to know they made the work's layout part of the contract.
func shapeNotice(crits []api.WorkItemCriterion) string {
	type pin struct {
		cmd    string
		shapes []string
	}
	var pins []pin
	for _, c := range crits {
		cmd := strings.TrimSpace(c.Command)
		if cmd == "" {
			continue
		}
		if s := pinnedShapes(cmd); len(s) > 0 {
			pins = append(pins, pin{cmd: cmd, shapes: s})
		}
	}
	if len(pins) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nNOTE — this plan's checks pin a SHAPE, not just an outcome:\n")
	for _, p := range pins {
		b.WriteString("  " + p.cmd + "\n")
		for _, s := range p.shapes {
			b.WriteString("    pins " + s + "\n")
		}
	}
	b.WriteString(
		"\nThat is allowed and it will work — the builder is shown every command verbatim, so it can\n" +
			"name things to match. But the work now has to be SHAPED this way to pass, even if a better\n" +
			"layout exists. Keep it when the shape is a real requirement; if you only meant to assert the\n" +
			"RESULT, a check that runs the behaviour through an entry point that already exists proves the\n" +
			"same thing and leaves the builder free.")
	return b.String()
}
