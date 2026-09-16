package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// party_registrations.go — WHAT PARTY MCP SERVERS ARE REGISTERED ON THIS MACHINE.
//
// `ptln join-mcp` writes a party into an engine's config so the session you're coding in can
// read and post. Those registrations OUTLIVE the party: the config entry is permanent, the
// party is not. They also accumulate somewhere you never look, in more than one file, one per
// party you have ever joined — so the honest scope of the question "what am I still wired to?"
// is the machine, not this repo.
//
// This file only FINDS them. It reads the same files join-mcp writes (and the two it prints
// setup for), parses out our entries, and returns them. It writes nothing, ever: removal is a
// command we print for a human to run, because these are the user's own config files and
// partyline editing another tool's config unasked is the behaviour that makes a CLI untrusted.
//
// OURS IS DECIDED BY THE ENVIRONMENT, NOT THE NAME. The server name is a `--server` flag, so
// two registrations of ours can have different names and someone else's server can have ours.
// The env var PARTYLINE_PARTY_ID is what actually makes an entry a partyline party MCP server —
// it is what party-mcp reads to know which party it is serving.

// partyRegistration is one party MCP server entry found in one config file.
//
// The party token is deliberately an UNEXPORTED field: encoding/json cannot reach it, so no
// amount of "just marshal the report" can leak a live party credential into a terminal
// transcript, a log, or a script's stdout. Nothing in this package prints it either.
type partyRegistration struct {
	Source    string `json:"source"`     // the config file this came from
	Scope     string `json:"scope"`      // which scope inside that file
	Server    string `json:"server"`     // the MCP server name it is registered under
	PartyID   string `json:"party_id"`   //
	Base      string `json:"base"`       // control plane the party lives on
	AgentName string `json:"agent_name"` // the @name this session posts as
	Status    string `json:"status"`     // live | ended | uncheckable — filled in by the probe
	Remove    string `json:"remove"`     // what a human runs to get rid of it

	token string // never exported, never printed
}

// The three words this listing answers in. "uncheckable" is api.PartyUnreachable said in the
// words a reader of a listing needs: it is a statement about our knowledge, not about the host.
const (
	statusLive        = "live"
	statusEnded       = "ended"
	statusUncheckable = "uncheckable"
)

// mcpConfigEntry is the shape every JSON-config engine uses for one server. Only env matters
// here — command/args tell us nothing about which party this is.
type mcpConfigEntry struct {
	Env map[string]string `json:"env"`
}

// scanPartyRegistrations finds every party MCP registration reachable from this machine: the
// engine configs join-mcp writes to (including claude's PER-DIRECTORY scope, which is what
// `--scope local` writes and the reason a repo-scoped listing would show you one entry and let
// you believe that was all of them), plus a project-local .mcp.json in dir.
//
// home and dir are parameters rather than looked up here so the whole scan is testable against
// a temporary tree.
func scanPartyRegistrations(home, dir string) []partyRegistration {
	var out []partyRegistration

	claudePath := filepath.Join(home, ".claude.json")
	if raw, err := os.ReadFile(claudePath); err == nil {
		var doc struct {
			MCPServers map[string]mcpConfigEntry `json:"mcpServers"`
			Projects   map[string]struct {
				MCPServers map[string]mcpConfigEntry `json:"mcpServers"`
			} `json:"projects"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			out = append(out, fromJSONServers(claudePath, "user", doc.MCPServers, func(server string) string {
				return "claude mcp remove " + server + " --scope user"
			})...)
			for projDir, p := range doc.Projects {
				out = append(out, fromJSONServers(claudePath, "directory "+projDir, p.MCPServers, func(server string) string {
					return "(cd " + projDir + " && claude mcp remove " + server + " --scope local)"
				})...)
			}
		}
	}

	geminiPath := filepath.Join(home, ".gemini", "settings.json")
	if raw, err := os.ReadFile(geminiPath); err == nil {
		var doc struct {
			MCPServers map[string]mcpConfigEntry `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			out = append(out, fromJSONServers(geminiPath, "user", doc.MCPServers, removeByHand(geminiPath))...)
		}
	}

	codexPath := filepath.Join(home, ".codex", "config.toml")
	if raw, err := os.ReadFile(codexPath); err == nil {
		out = append(out, fromJSONServers(codexPath, "user", codexMCPEnv(raw), removeByHand(codexPath))...)
	}

	projectPath := filepath.Join(dir, ".mcp.json")
	if raw, err := os.ReadFile(projectPath); err == nil {
		var doc struct {
			MCPServers map[string]mcpConfigEntry `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			out = append(out, fromJSONServers(projectPath, "project", doc.MCPServers, removeByHand(projectPath))...)
		}
	}

	// Stable order, so two runs on an unchanged machine print the same thing and a diff of the
	// JSON output means something changed.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Server < out[j].Server
	})
	return out
}

// removeByHand is the removal instruction for a config we do not have a remove COMMAND for.
// Naming the file and the entry beats guessing at an engine's subcommand we have not verified.
func removeByHand(path string) func(string) string {
	return func(server string) string {
		return "delete the " + server + " entry from " + path
	}
}

// fromJSONServers turns one config's server map into our registrations — the ones carrying a
// PARTYLINE_PARTY_ID, whatever they are named.
func fromJSONServers(source, scope string, servers map[string]mcpConfigEntry, remove func(string) string) []partyRegistration {
	var out []partyRegistration
	for name, entry := range servers {
		id := entry.Env["PARTYLINE_PARTY_ID"]
		if id == "" {
			continue
		}
		out = append(out, partyRegistration{
			Source:    source,
			Scope:     scope,
			Server:    name,
			PartyID:   id,
			Base:      entry.Env["PARTYLINE_PARTY_BASE"],
			AgentName: entry.Env["PARTYLINE_AGENT_NAME"],
			token:     entry.Env["PARTYLINE_PARTY_TOKEN"],
			Status:    statusUncheckable,
			Remove:    remove(name),
		})
	}
	return out
}

// codexMCPEnv pulls the env of every mcp_servers block out of ~/.codex/config.toml, in the same
// shape the JSON engines use. Like doctor_mcp.go's reader this is deliberately not a TOML
// parser: the two forms partyline itself prints — dotted keys and an [mcp_servers.x.env] table —
// are the two forms that appear, and a dependency for reading four env vars would be its own
// kind of cost.
func codexMCPEnv(raw []byte) map[string]mcpConfigEntry {
	out := map[string]mcpConfigEntry{}
	put := func(server, key, val string) {
		e, ok := out[server]
		if !ok {
			e = mcpConfigEntry{Env: map[string]string{}}
			out[server] = e
		}
		e.Env[key] = val
	}
	table := "" // the server whose [mcp_servers.<name>.env] table we are inside, if any
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			table = ""
			if head := strings.Trim(line, "[]"); strings.HasPrefix(head, "mcp_servers.") && strings.HasSuffix(head, ".env") {
				table = strings.TrimSuffix(strings.TrimPrefix(head, "mcp_servers."), ".env")
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(val), `"`)
		switch {
		case table != "":
			put(table, key, val)
		case strings.HasPrefix(key, "mcp_servers."):
			// mcp_servers.<server>.env.<KEY>
			rest := strings.TrimPrefix(key, "mcp_servers.")
			server, field, ok := strings.Cut(rest, ".env.")
			if ok && server != "" {
				put(server, field, val)
			}
		}
	}
	return out
}
