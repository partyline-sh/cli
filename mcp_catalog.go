package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The MCP catalog (E2.1): the user's named MCP servers, kept in ONE file the ctrl-\ m menu
// reads and writes. A definition is either a stdio server (command+args+env) or an HTTP one
// (url+headers). The catalog is only definitions — which servers a given SESSION gets lives
// on that session's Spec (Spec.MCPs), so two sessions can run different sets.
type mcpDef struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Board marks a server that also serves a BOARD partyline can show alongside its own — an Odoo
	// project, a Jira board, a Linear team. See docs/plans/board-providers.md.
	//
	// Opt-in rather than probed: spawning every catalogued server on every board launch to ask
	// whether it happens to be a board would be slow, and rude to the servers that are not.
	Board boardConfig `json:"board,omitempty"`
}

// boardConfig says HOW to get a board out of a server. It accepts two JSON shapes:
//
//	"board": true                          the server implements partyline://board itself
//	"board": {"kind": "odoo", ...}         partyline drives the server's own tools
//
// The second exists because most trackers already have an MCP server that nobody wants to modify
// — a shared production server with other consumers has no business carrying partyline-specific
// surface. For those, the knowledge of how to assemble a board lives here, and what the server
// gets asked is an ordinary tool call it already answers.
//
// Per-installation conventions belong in Opts, never in the binary. "Which stage names mean done"
// and "strip this suffix the stage set was duplicated with" are one company's artifacts; the next
// Odoo partyline meets will have different ones.
type boardConfig struct {
	Enabled bool
	Kind    string            // "" = the partyline://board resource contract
	Opts    map[string]string // kind-specific knobs
	Poll    int               // minutes between automatic re-reads; 0 = manual g only
}

func (b boardConfig) Opt(key, def string) string {
	if v := strings.TrimSpace(b.Opts[key]); v != "" {
		return v
	}
	return def
}

func (b *boardConfig) UnmarshalJSON(data []byte) error {
	// The bare `true` that every existing catalog uses has to keep meaning what it meant.
	var flag bool
	if json.Unmarshal(data, &flag) == nil {
		b.Enabled = flag
		return nil
	}
	var obj struct {
		Kind string            `json:"kind"`
		Opts map[string]string `json:"opts"`
		Poll int               `json:"poll_minutes"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf(`"board" must be true or an object like {"kind":"odoo"}: %w`, err)
	}
	b.Enabled, b.Kind, b.Opts, b.Poll = true, strings.TrimSpace(obj.Kind), obj.Opts, obj.Poll
	return nil
}

func (b boardConfig) MarshalJSON() ([]byte, error) {
	if !b.Enabled {
		return []byte("false"), nil
	}
	if b.Kind == "" && len(b.Opts) == 0 && b.Poll == 0 {
		return []byte("true"), nil // round-trips the simple form unchanged
	}
	return json.Marshal(struct {
		Kind string            `json:"kind,omitempty"`
		Opts map[string]string `json:"opts,omitempty"`
		Poll int               `json:"poll_minutes,omitempty"`
	}{b.Kind, b.Opts, b.Poll})
}

type mcpCatalog map[string]mcpDef // name → definition

func mcpCatalogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "mcp.json")
}

// loadMCPCatalog reads the catalog; a missing file is an empty catalog, never an error.
func loadMCPCatalog() mcpCatalog {
	b, err := os.ReadFile(mcpCatalogPath())
	if err != nil {
		return mcpCatalog{}
	}
	var f struct {
		Servers mcpCatalog `json:"servers"`
	}
	if json.Unmarshal(b, &f) != nil || f.Servers == nil {
		return mcpCatalog{}
	}
	return f.Servers
}

func saveMCPCatalog(cat mcpCatalog) error {
	p := mcpCatalogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Servers mcpCatalog `json:"servers"`
	}{cat}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// names returns the catalog's server names, sorted — stable menu numbering.
func (cat mcpCatalog) names() []string {
	out := make([]string, 0, len(cat))
	for n := range cat {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// mcpServersJSON builds claude's --mcp-config value: ONE merged mcpServers object holding the
// partyline context-threads server (when a thread is attached) plus every selected catalog
// server. One flag, rebuilt from state each (re)launch — never incrementally patched.
func mcpServersJSON(thread bool, mcps []string, cat mcpCatalog) string {
	servers := map[string]any{}
	if thread {
		servers["partyline-context-threads"] = map[string]any{"command": selfExe(), "args": []string{"cg-mcp"}}
	}
	for _, n := range mcps {
		def, ok := cat[n]
		if !ok {
			continue // deleted from the catalog since it was toggled on — drop silently
		}
		servers[n] = catalogServerDef(def)
	}
	if len(servers) == 0 {
		return ""
	}
	b, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return ""
	}
	return string(b)
}

// mcpCodexFlags builds codex's `-c mcp_servers.<name>.…` override pairs for the selected
// catalog servers. codex takes TOML dotted overrides per launch; HTTP servers aren't
// wireable this way, so they're skipped (the menu says so).
func mcpCodexFlags(mcps []string, cat mcpCatalog) []string {
	var out []string
	for _, n := range mcps {
		def, ok := cat[n]
		if !ok || def.URL != "" || def.Command == "" {
			continue
		}
		out = append(out, "-c", fmt.Sprintf("mcp_servers.%s.command=%q", n, def.Command))
		if len(def.Args) > 0 {
			ab, _ := json.Marshal(def.Args)
			out = append(out, "-c", fmt.Sprintf("mcp_servers.%s.args=%s", n, ab))
		}
		for k, v := range def.Env {
			out = append(out, "-c", fmt.Sprintf("mcp_servers.%s.env.%s=%q", n, k, v))
		}
	}
	return out
}

// mcpManagedCodexOverride reports whether a codex `-c` value is one partyline manages —
// the context-threads server (current or legacy name) or any catalog server — so the
// strip-and-rebuild never touches overrides the user passed themselves.
func mcpManagedCodexOverride(val string, cat mcpCatalog) bool {
	if !strings.HasPrefix(val, "mcp_servers.") {
		return false
	}
	rest := strings.TrimPrefix(val, "mcp_servers.")
	name := rest
	if i := strings.IndexByte(rest, '.'); i >= 0 {
		name = rest[:i]
	}
	if name == "partyline-context-threads" || name == "common-ground" {
		return true
	}
	_, ok := cat[name]
	return ok
}

// catalogServerDef renders one catalog entry in claude's mcpServers JSON shape — shared by
// session wiring (mcpServersJSON) and launched-agent tool grants (resolveLaunchGrants).
func catalogServerDef(def mcpDef) map[string]any {
	s := map[string]any{}
	if def.URL != "" {
		s["type"] = "http"
		s["url"] = def.URL
		if len(def.Headers) > 0 {
			s["headers"] = def.Headers
		}
		return s
	}
	s["command"] = def.Command
	if len(def.Args) > 0 {
		s["args"] = def.Args
	}
	if len(def.Env) > 0 {
		s["env"] = def.Env
	}
	return s
}

// readCatalogRaw / writeCatalogRaw edit the catalog file as free-form JSON.
//
// Deliberately not a round-trip through mcpDef: this file is hand-edited and also written by the
// ctrl-\ m menu, and re-serializing it from a struct would silently drop any key this binary does
// not happen to model. A board setting must not cost somebody the rest of their config.
func readCatalogRaw() (map[string]any, error) {
	b, err := os.ReadFile(mcpCatalogPath())
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("~/.partyline/mcp.json is not valid JSON: %w", err)
	}
	return raw, nil
}

func writeCatalogRaw(raw map[string]any) error {
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mcpCatalogPath()), 0o755); err != nil {
		return err
	}
	// Written through a temp file: a catalog truncated by a crash mid-write would take every MCP
	// server on the machine with it, not just the board.
	tmp := mcpCatalogPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, mcpCatalogPath())
}
