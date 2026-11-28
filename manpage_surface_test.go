package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/clispec"
)

// The man page is hand-written prose, deliberately: a manual explains a trust model and a set of
// worked examples, which no generator produces. What a generator WOULD have given for free is the
// guarantee that it names the real commands and the real tools. These tests give that guarantee
// without turning the manual into a dump.
//
// They exist because an audit found `ptln trigger` documented here a full release after the
// command, its API routes and its settings panel were all deleted, and found no section at all for
// memory, bus or feed months after they shipped.

func manPageText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("docs/partyline.1")
	if err != nil {
		t.Fatalf("reading the man page: %v", err)
	}
	// Normalise roff escapes so `\fBptln invite\-machine\fR` matches "ptln invite-machine".
	text := regexp.MustCompile(`\\f[BIPR]`).ReplaceAllString(string(b), "")
	return strings.ReplaceAll(text, `\-`, "-")
}

func TestManPageNamesEveryCommand(t *testing.T) {
	text := manPageText(t)
	// A heading may combine related commands — `.B ptln logout | whoami` — so each alternative
	// counts as naming its own command.
	named := map[string]bool{}
	for _, m := range regexp.MustCompile(`ptln ((?:[a-z][a-z-]{1,20})(?: \| [a-z][a-z-]{1,20})*)`).FindAllStringSubmatch(text, -1) {
		for _, alt := range strings.Split(m[1], " | ") {
			named[strings.TrimSpace(alt)] = true
		}
	}
	var missing []string
	for _, c := range clispec.Visible() {
		if !named[c.Name] {
			missing = append(missing, c.Name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("docs/partyline.1 never mentions: %v", missing)
	}
}

// A command in the manual that the binary does not dispatch is worse than a missing one: the
// reader types it, and is told it does not exist.
func TestManPageNamesNoPhantomCommand(t *testing.T) {
	text := manPageText(t)
	real := map[string]bool{}
	for _, c := range clispec.Commands {
		real[c.Name] = true
		for _, a := range c.Aliases {
			real[a] = true
		}
	}
	var phantom []string
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`ptln ([a-z][a-z-]{1,20})`).FindAllStringSubmatch(text, -1) {
		v := m[1]
		if real[v] || seen[v] {
			continue
		}
		seen[v] = true
		phantom = append(phantom, v)
	}
	// SUBCOMMANDS TOO. `ptln project tools` passed this test for a whole pass, because `project`
	// is real and the check stopped at the first word — so an EXAMPLES block for a subcommand that
	// was never dispatched sat in the manual unnoticed.
	for _, gone := range []string{"project tools", "project doc", "project env", "trigger"} {
		if strings.Contains(text, "ptln "+gone) {
			phantom = append(phantom, "ptln "+gone)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Errorf("docs/partyline.1 documents commands that do not exist: %v", phantom)
	}
}
