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
		s := map[string]any{}
		if def.URL != "" {
			s["type"] = "http"
			s["url"] = def.URL
			if len(def.Headers) > 0 {
				s["headers"] = def.Headers
			}
		} else {
			s["command"] = def.Command
			if len(def.Args) > 0 {
				s["args"] = def.Args
			}
			if len(def.Env) > 0 {
				s["env"] = def.Env
			}
		}
		servers[n] = s
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
