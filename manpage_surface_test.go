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

// An MCP tool name ANYWHERE in the manual must be one the server advertises. The section-scoped
// check below missed `create_project` sitting in the COMMANDS prose, one screen away.
func TestManPageNamesNoPhantomTool(t *testing.T) {
	text := manPageText(t)
	advertised := map[string]bool{}
	for _, d := range cgToolDefs {
		name, _ := d["name"].(string)
		advertised[name] = true
	}
	// Tools that were removed and must never be documented again. A general snake_case sweep over
	// the whole manual has too many false positives (env vars, arguments, git trailers); an explicit
	// gravestone list is what these three earned.
	for _, gone := range []string{"create_project", "propose_work_item", "plan_file_tree", "import_work_item"} {
		if advertised[gone] {
			t.Fatalf("%q is advertised again — remove it from the gravestone list", gone)
		}
		if strings.Contains(text, gone) {
			t.Errorf("docs/partyline.1 names %q, a tool that does not exist", gone)
		}
	}
}

// The MCP TOOLS section must name exactly what the server advertises — see cgToolDefs.
func TestManPageMCPToolsMatchesTheServer(t *testing.T) {
	text := manPageText(t)
	start := strings.Index(text, ".SH MCP TOOLS")
	if start < 0 {
		t.Fatal("docs/partyline.1 has no MCP TOOLS section")
	}
	rest := text[start+len(".SH MCP TOOLS"):]
	if end := strings.Index(rest, "\n.SH "); end >= 0 {
		rest = rest[:end]
	}
	advertised := map[string]bool{}
	for _, d := range cgToolDefs {
		name, _ := d["name"].(string)
		advertised[name] = true
		if !strings.Contains(rest, name) {
			t.Errorf("MCP TOOLS does not document the advertised tool %q", name)
		}
	}
	// Anything snake_case in that section that is not an advertised tool is a phantom, with the
	// tools' own ARGUMENT names allowed through — those are documented, not claimed to be tools.
	allowed := map[string]bool{
		"partyline_context_threads": true,
		"instance_name":             true,
		"allow_signups":             true,
	}
	for _, m := range regexp.MustCompile(`\b([a-z]+_[a-z_]+)\b`).FindAllStringSubmatch(rest, -1) {
		w := m[1]
		if advertised[w] || allowed[w] {
			continue
		}
		t.Errorf("MCP TOOLS names %q, which the server does not advertise", w)
	}
}
