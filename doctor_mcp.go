package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// doctor_mcp.go — the MCP-wiring section of `ptln daemon doctor` (#558, MCP-6 of epic #552).
//
// "Every LLM loads the partyline MCP" must be PROVABLE, not hoped. The first-run offer (#557)
// wires engines once; nothing afterwards ever verified the wiring still holds — and it can rot in
// ways the user cannot see from partyline's side: the engine reinstalled, its config reset, or the
// registered binary path pointing at a file that no longer exists. That last one is not
// hypothetical: a /tmp build was once registered into three real engine configs, and the symptom
// (recall/remember silently gone) pointed nowhere near the cause. Doctor now names it.
//
// Read-only by design, like the rest of doctor: it READS each engine's config file and reports;
// the fix is always `ptln thread connect <engine>`, never an edit made here.

const mcpServerName = "partyline-context-threads"

// mcpCommandFromClaudeJSON pulls the registered command path out of a ~/.claude.json (also the
// shape ~/.gemini/settings.json uses). Returns "" when the server isn't registered. Pure, so the
// parsing rule is testable without a home directory.
func mcpCommandFromClaudeJSON(raw []byte) string {
	var doc struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return doc.MCPServers[mcpServerName].Command
}

// codexMCPCommandRe finds the command line of our server's TOML block. TOML section headers reset
// the scope, so the match is anchored to the partyline block and stops at the next '['.
var codexMCPCommandRe = regexp.MustCompile(`(?s)\[mcp_servers\.` + regexp.QuoteMeta(mcpServerName) + `[^\[]*?command\s*=\s*"([^"]+)"`)

// mcpCommandFromCodexTOML pulls the registered command out of ~/.codex/config.toml. A full TOML
// parser for one string would be a dependency for nothing; the shape `codex mcp add` writes is
// stable and the regex is anchored to our own server's block.
func mcpCommandFromCodexTOML(raw []byte) string {
	m := codexMCPCommandRe.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// mcpWiringVerdict is the per-engine rule: given the registered command path (or ""), what does
// doctor say? Extracted pure so the three outcomes — not wired, wired-to-a-ghost, wired — are
// testable as a rule rather than exercised by hand against real configs.
func mcpWiringVerdict(cmdPath string, exists bool) (checkStatus, string) {
	switch {
	case cmdPath == "":
		return ckFail, "not wired — recall/remember won't exist in its sessions"
	case !exists:
		// The v0.37.x failure mode: registration intact, binary gone. Worse than unwired,
		// because everything LOOKS configured.
		return ckFail, "wired to a binary that no longer exists (" + cmdPath + ")"
	case isEphemeralPath(cmdPath):
		return ckWarn, "wired to a temporary path (" + cmdPath + ") — will break when it's cleaned up"
	default:
		return ckPass, "registered (" + cmdPath + ")"
	}
}

// mcpWiring is one installed engine's wiring verdict — probed from its real config file, not
// from the first-run offer state, so a manual `ptln thread connect` (or a rotted registration)
// reads true. Shared by doctor (prints every row) and setup (summarizes).
type mcpWiring struct {
	name   string // human name
	fix    string // the connect command that repairs it
	detail string
	status checkStatus
}

// mcpWirings probes each installed engine's config. Only engines actually on this machine are
// checked — reporting an engine the user doesn't have is noise.
func mcpWirings() []mcpWiring {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	type probe struct {
		name string // human name
		bin  string // detect via PATH
		file string // config file to read
		toml bool   // codex speaks TOML; the others the claude.json shape
		fix  string
	}
	probes := []probe{
		{"Claude Code", "claude", filepath.Join(home, ".claude.json"), false, "ptln thread connect claude"},
		{"Codex", "codex", filepath.Join(home, ".codex", "config.toml"), true, "ptln thread connect codex"},
		{"Gemini CLI", "gemini", filepath.Join(home, ".gemini", "settings.json"), false, "ptln thread connect gemini"},
	}
	var out []mcpWiring
	for _, p := range probes {
		if _, err := exec.LookPath(p.bin); err != nil {
			continue
		}
		raw, _ := os.ReadFile(p.file)
		var cmd string
		if p.toml {
			cmd = mcpCommandFromCodexTOML(raw)
		} else {
			cmd = mcpCommandFromClaudeJSON(raw)
		}
		exists := false
		if cmd != "" {
			// The command may be a bare name on PATH ("ptln") or an absolute path; both count as
			// existing when resolvable.
			if strings.Contains(cmd, string(filepath.Separator)) {
				_, statErr := os.Stat(cmd)
				exists = statErr == nil
			} else {
				_, lookErr := exec.LookPath(cmd)
				exists = lookErr == nil
			}
		}
		status, detail := mcpWiringVerdict(cmd, exists)
		out = append(out, mcpWiring{p.name, p.fix, detail, status})
	}
	return out
}

// doctorMCPWiring prints the per-engine wiring rows.
func doctorMCPWiring() {
	fmt.Println("\n  context MCP wiring (recall/remember in your AI CLIs)")
	wirings := mcpWirings()
	anyInstalled := len(wirings) > 0
	for _, w := range wirings {
		w.status.line(w.name, w.detail, w.fix)
	}
	// Antigravity has no config of its own to read — it imports claude's. Say so rather than
	// pretending to have checked something.
	if _, err := exec.LookPath("agy"); err == nil {
		anyInstalled = true
		fmt.Println("  · Antigravity imports Claude Code's wiring — if claude is ✓ above, re-import with `agy plugin import claude` after any change")
	}
	if !anyInstalled {
		fmt.Println("  · no AI CLIs found on PATH (claude / codex / gemini / agy)")
	}
}
