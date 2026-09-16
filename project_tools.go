package main

import (
	"fmt"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// ptln project tools — the terminal editor for #574 agent tool grants (CLI parity with the web
// Agent tools panel; the founder's rule: every web settings surface gets an ascii CLI too).
//
//	ptln project tools <label>                         show the project's grants
//	ptln project tools <label> --role planning --allow-shell "gh *" [--allow-mcp linear]
//	ptln project tools <label> --role build    --revoke-shell "gh *" [--revoke-mcp linear]
//
// Grants are names/prefixes only (pure DATA) — the machine that RUNS an agent resolves them
// against its own local catalog; the review role is deliberately not editable (verifier ≠
// producer). The server re-validates every entry and names a bad one.
func projectTools(c *api.Client, args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf(`usage: ptln project tools <label> [--role planning|build] [--allow-shell "<prefix>"] [--revoke-shell "<prefix>"] [--allow-mcp <name>] [--revoke-mcp <name>]`))
	}
	label := strings.TrimSpace(args[0])
	role := "planning"
	var allowShell, revokeShell, allowMCP, revokeMCP []string
	for i := 1; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return strings.TrimSpace(args[i])
			}
			fatal(fmt.Errorf("%s needs a value", args[i]))
			return ""
		}
		switch args[i] {
		case "--role":
			role = strings.ToLower(next())
		case "--allow-shell":
			allowShell = append(allowShell, next())
		case "--revoke-shell":
			revokeShell = append(revokeShell, next())
		case "--allow-mcp":
			allowMCP = append(allowMCP, next())
		case "--revoke-mcp":
			revokeMCP = append(revokeMCP, next())
		default:
			fatal(fmt.Errorf("unknown flag %q — run `ptln project help`", args[i]))
		}
	}
	if role != "planning" && role != "build" {
		fatal(fmt.Errorf("--role must be planning or build (review is deliberately not grantable — the verifier stays independent)"))
	}
	proj := projectForLabel(c, label)
	grants := proj.ToolGrants
	if grants == nil {
		grants = map[string]api.ToolGrants{}
	}
	if len(allowShell)+len(revokeShell)+len(allowMCP)+len(revokeMCP) == 0 {
		printGrants(proj.Label, grants)
		return
	}
	g := grants[role]
	g.Shell = editList(g.Shell, allowShell, revokeShell)
	g.MCP = editList(g.MCP, allowMCP, revokeMCP)
	grants[role] = g
	if err := c.UpdateProjectToolGrants(proj.ID, grants); err != nil {
		fatal(fmt.Errorf("save failed: %w", err)) // server names the offending entry
	}
	fmt.Printf("✓ %s · %s grants saved\n", proj.Label, role)
	printGrants(proj.Label, grants)
	fmt.Println("  applies to the NEXT agent launch/run on this project (live agents keep their posture)")
}

// projectForLabel resolves a label (or id prefix) against the team's projects — exact label
// match first, then unique id prefix. Mirrors the exact-match posture used everywhere else.
func projectForLabel(c *api.Client, label string) *api.Project {
	projects, err := c.ListProjects()
	if err != nil {
		fatal(err)
	}
	for i := range projects {
		if projects[i].Label == label {
			// The list row may be a thin projection — fetch the full record (grants included).
			if full, _, err := c.GetProject(projects[i].ID); err == nil && full != nil {
				return full
			}
			return &projects[i]
		}
	}
	var byID *api.Project
	for i := range projects {
		if strings.HasPrefix(projects[i].ID, label) {
			if byID != nil {
				fatal(fmt.Errorf("id prefix %q is ambiguous", label))
			}
			byID = &projects[i]
		}
	}
	if byID == nil {
		fatal(fmt.Errorf("no project labeled %q — `ptln project` lists them", label))
	}
	if full, _, err := c.GetProject(byID.ID); err == nil && full != nil {
		return full
	}
	return byID
}

// editList applies adds then removes, dedupes, and keeps a stable order.
func editList(cur, add, remove []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(cur)+len(add))
	for _, v := range append(append([]string{}, cur...), add...) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	drop := map[string]bool{}
	for _, v := range remove {
		drop[strings.TrimSpace(v)] = true
	}
	kept := out[:0]
	for _, v := range out {
		if !drop[v] {
			kept = append(kept, v)
		}
	}
	return kept
}

func printGrants(label string, grants map[string]api.ToolGrants) {
	fmt.Printf("%s — agent tool grants (names/prefixes only; each machine resolves locally)\n", label)
	for _, role := range []string{"planning", "build"} {
		g := grants[role]
		fmt.Printf("  %-9s shell: %s\n", role, orNone(g.Shell))
		fmt.Printf("            mcp:   %s\n", orNone(g.MCP))
	}
	fmt.Println("  review    (not grantable — the verifier keeps its read-only tools)")
	if cat := loadMCPCatalog(); len(cat) > 0 {
		names := make([]string, 0, len(cat))
		for n := range cat {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("  this machine's MCP catalog: %s\n", strings.Join(names, ", "))
	}
}

func orNone(v []string) string {
	if len(v) == 0 {
		return "—"
	}
	return strings.Join(v, " · ")
}
