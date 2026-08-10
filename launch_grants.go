package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// #574 slice 3 — carry the org's per-project TOOL GRANTS to a launched planning agent.
//
// The control plane sends only DATA: MCP server NAMES and shell command PREFIXES an org admin
// granted in the project's Agent tools panel. This file is where that data meets capability,
// and the reference-not-command invariant holds at both halves:
//   - an MCP name selects an entry from the daemon's OWN local catalog (~/.partyline/mcp.json);
//     a name with no local entry is skipped with a note — the server can never introduce a
//     command, only point at one the machine's owner already configured.
//   - a shell prefix becomes an entry INSIDE claude's --allowedTools value (permission-engine
//     data, never shell-interpreted; argv is exec'd as a vector). It is re-validated here with
//     the same strict shape the web enforces — the daemon never trusts the server's validation.
//
// Fail-closed: anything invalid or unknown is skipped (with a note the daemon console shows),
// never widened. The review agent's launch path never reads grants (verifier ≠ producer).
var (
	grantNameRe   = regexp.MustCompile(`^(?i)[a-z0-9][a-z0-9._-]{0,63}$`)
	grantPrefixRe = regexp.MustCompile(`^(?i)[a-z0-9][a-z0-9._-]{0,40}( [a-z0-9._:*-]{1,40}){0,4}( \*)?$`)
)

const maxGrantEntries = 20 // mirrors the web cap; a longer list is truncated, not rejected

// resolveLaunchGrants turns grant DATA into claude launch flags: extra --allowedTools entries
// ("gh *" → "Bash(gh:*)", "gh api user" → "Bash(gh api user)", each granted MCP server →
// "mcp__<name>") plus one --mcp-config JSON built ONLY from local catalog definitions.
func resolveLaunchGrants(g *api.ToolGrants, cat mcpCatalog) (allow []string, mcpConfig string, notes []string) {
	if g == nil {
		return nil, "", nil
	}
	shell := g.Shell
	if len(shell) > maxGrantEntries {
		shell = shell[:maxGrantEntries]
		notes = append(notes, fmt.Sprintf("shell grants truncated to %d", maxGrantEntries))
	}
	for _, p := range shell {
		p = strings.TrimSpace(p)
		if !grantPrefixRe.MatchString(p) {
			notes = append(notes, fmt.Sprintf("shell grant %q: invalid shape — skipped", p))
			continue
		}
		if base, ok := strings.CutSuffix(p, " *"); ok {
			allow = append(allow, "Bash("+base+":*)") // prefix grant: the command plus anything after
		} else {
			allow = append(allow, "Bash("+p+")") // exact-command grant
		}
	}
	names := g.MCP
	if len(names) > maxGrantEntries {
		names = names[:maxGrantEntries]
		notes = append(notes, fmt.Sprintf("mcp grants truncated to %d", maxGrantEntries))
	}
	servers := map[string]any{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if !grantNameRe.MatchString(n) {
			notes = append(notes, fmt.Sprintf("mcp grant %q: invalid name — skipped", n))
			continue
		}
		def, ok := cat[n]
		if !ok {
			notes = append(notes, fmt.Sprintf("mcp grant %q: not in this machine's catalog (~/.partyline/mcp.json) — skipped", n))
			continue
		}
		servers[n] = catalogServerDef(def)
		allow = append(allow, "mcp__"+n) // whole-server allow for the granted server
	}
	if len(servers) > 0 {
		if b, err := json.Marshal(map[string]any{"mcpServers": servers}); err == nil {
			mcpConfig = string(b)
		}
	}
	return allow, mcpConfig, notes
}
