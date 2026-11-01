package main

// PER-TURN INJECTION ON ENGINES THAT ARE NOT CLAUDE.
//
// The memory map in AGENTS.md/CLAUDE.md/GEMINI.md is the floor: every engine reads its own file at
// session start, with no trust prompt and no model discretion. What it cannot do is refresh. This
// machine has had agent sessions alive for a fortnight, so a session briefed on Monday never learns
// what a teammate recorded on Thursday. That is what the hook is for, and it fires every turn.
//
// THE THING THAT MAKES THIS DANGEROUS TO SHIP QUIETLY: three of the four engines refuse hooks until
// a directory or hook is trusted, and codex degrades SILENTLY — `codex exec` runs with hooks
// disabled and says nothing. A partyline-installed hook will look installed, look correct, and
// inject nothing until someone opens the TUI once and approves it. An installer that does not say
// so is worse than no installer, because it converts a visible gap into an invisible one.
//
// So this file reports state it has actually checked and says plainly when it cannot check. It
// writes config only where it can do so safely:
//
//   - gemini, antigravity: JSON settings, written with encoding/json — safe to merge.
//   - codex: config.toml. There is no TOML writer in this module, and the established pattern here
//     is join_mcp.go, which PRINTS ready-to-paste codex config rather than rewriting a file whose
//     full grammar we do not parse. Mangling someone's codex config to save them a paste is a bad
//     trade.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hookEngine is one engine's pre-prompt injection point.
//
// The event names differ per engine and are NOT guesses: gemini's own `hooks migrate` command maps
// Claude's UserPromptSubmit onto BeforeAgent, and codex ships a JSON schema naming
// UserPromptSubmitHookSpecificOutputWire with an additionalContext field.
type hookEngine struct {
	name     string
	event    string // the engine's own name for "before the model sees the prompt"
	config   string // absolute path to the file that configures it ("" = configured at launch)
	trustDoc string // how a human grants trust, in their words not ours
}

func hookEngines() []hookEngine {
	home, _ := os.UserHomeDir()
	return []hookEngine{
		{
			name:  "claude",
			event: "UserPromptSubmit",
			// Claude's hooks are passed per session with --settings (see sessionSettings in
			// keepgoing.go), so there is no file to install into and no trust gate.
			config:   "",
			trustDoc: "",
		},
		{
			name:     "codex",
			event:    "UserPromptSubmit",
			config:   filepath.Join(home, ".codex", "config.toml"),
			trustDoc: "open `codex` once and answer \"Trust all and continue\" when it says a hook is new or changed",
		},
		{
			name:     "gemini",
			event:    "BeforeAgent",
			config:   filepath.Join(home, ".gemini", "settings.json"),
			trustDoc: "run gemini once in this directory and trust the workspace (or pass --skip-trust)",
		},
		{
			name:     "antigravity",
			event:    "PreInvocation",
			config:   filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"),
			trustDoc: "add this directory to trustedWorkspaces, or open antigravity here once and trust it",
		},
	}
}

// hookStatus is what we were able to establish about one engine, on this machine, right now.
type hookStatus struct {
	engine    string
	present   bool   // the engine is installed
	installed bool   // partyline's hook is in its config
	trusted   trisel // whether the engine will actually RUN it
	detail    string
}

// trisel is yes/no/unknown. Unknown is a first-class answer here: codex records hook trust as a
// hash we cannot recompute without reimplementing its hashing, and reporting "trusted" on a guess
// is precisely the silent failure this file exists to prevent.
type trisel int

const (
	triUnknown trisel = iota
	triYes
	triNo
)

func (t trisel) String() string {
	switch t {
	case triYes:
		return "yes"
	case triNo:
		return "NO"
	default:
		return "unknown"
	}
}

// memoryHookStatus inspects every engine. Read-only.
func memoryHookStatus(dir string) []hookStatus {
	var out []hookStatus
	for _, e := range hookEngines() {
		s := hookStatus{engine: e.name, present: engineInstalled(e.name)}
		switch e.name {
		case "claude":
			s.installed = true
			s.trusted = triYes
			s.detail = "wired per session at launch; no config file and no trust gate"
		case "codex":
			s.installed = fileContains(e.config, "memory-hook")
			// Deliberately unknown: codex stores hook trust as `trusted_hash`, and a stale hash
			// after the command changes puts it back to untrusted without touching the config.
			s.trusted = triUnknown
			s.detail = "codex runs `codex exec` with hooks DISABLED and no warning when a hook is untrusted"
		case "gemini":
			s.installed = jsonHookInstalled(e.config, e.event)
			s.trusted = geminiWorkspaceTrusted(e.config, dir)
		case "antigravity":
			s.installed = jsonHookInstalled(antigravityHooksFile(), e.event)
			s.trusted = antigravityWorkspaceTrusted(e.config, dir)
		}
		out = append(out, s)
	}
	return out
}

func engineInstalled(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileContains(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), needle)
}

// antigravityHooksFile is the hooks config at antigravity's customization root, which is a
// different file from its settings.json (that one holds trustedWorkspaces).
func antigravityHooksFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "hooks.json")
}

// jsonHookInstalled reports whether partyline's hook is registered for an event in a JSON config.
func jsonHookInstalled(path, event string) bool {
	m, err := readJSONFile(path)
	if err != nil {
		return false
	}
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	b, _ := json.Marshal(hooks[event])
	return strings.Contains(string(b), "memory-hook")
}

// geminiWorkspaceTrusted reads gemini's own trust record. Absent means gemini will ask, which for
// a headless run means the hook does not fire.
func geminiWorkspaceTrusted(path, dir string) trisel {
	m, err := readJSONFile(path)
	if err != nil {
		return triUnknown
	}
	if os.Getenv("GEMINI_CLI_TRUST_WORKSPACE") != "" {
		return triYes
	}
	folders, ok := m["trustedFolders"]
	if !ok {
		return triUnknown
	}
	return dirCoveredBy(folders, dir)
}

func antigravityWorkspaceTrusted(path, dir string) trisel {
	m, err := readJSONFile(path)
	if err != nil {
		return triUnknown
	}
	ws, ok := m["trustedWorkspaces"]
	if !ok {
		return triNo
	}
	return dirCoveredBy(ws, dir)
}

// dirCoveredBy reports whether dir sits under any trusted root. Trust is inherited by
// subdirectories in both gemini and antigravity, so a trusted home directory covers every repo in
// it — which is why this is a prefix test and not equality.
func dirCoveredBy(v any, dir string) trisel {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return triUnknown
	}
	list, ok := v.([]any)
	if !ok {
		return triUnknown
	}
	for _, it := range list {
		root, _ := it.(string)
		if root == "" {
			continue
		}
		if abs == root || strings.HasPrefix(abs+string(filepath.Separator), strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
			return triYes
		}
	}
	return triNo
}

func readJSONFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// installJSONHook merges partyline's hook into a JSON config, preserving everything already there.
//
// Merging rather than replacing is not politeness: these files hold the user's MCP servers and
// their auth selection, and a settings file written from scratch would silently log them out.
func installJSONHook(path, event, command string) error {
	m, err := readJSONFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("%s is not readable as JSON, so it was left alone: %w", path, err)
		}
		m = map[string]any{}
	}
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), command) {
			return nil // already installed; re-running must not stack duplicates
		}
	}
	hooks[event] = append(entries, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	})
	m["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// codexHookSnippet is what a human pastes into ~/.codex/config.toml.
func codexHookSnippet() string {
	return "[[hooks.UserPromptSubmit]]\n" +
		"[[hooks.UserPromptSubmit.hooks]]\n" +
		"type = \"command\"\n" +
		"command = \"" + selfExe() + " memory-hook\"\n"
}

// memoryHooksCmd is `ptln memory hooks [--install]`.
//
// The default is a REPORT, not an install. The useful output of this command is usually "codex has
// the hook but may not be running it", which no amount of installing fixes.
func memoryHooksCmd(args []string) {
	install := false
	for _, a := range args {
		if a == "--install" {
			install = true
		}
	}
	dir, _ := os.Getwd()
	root, isRepo := repoMemory(dir)
	if isRepo {
		dir = root
	}

	if install {
		for _, e := range hookEngines() {
			if e.config == "" || !engineInstalled(e.name) {
				continue
			}
			switch e.name {
			case "gemini":
				reportInstall(e.name, e.config, installJSONHook(e.config, e.event, selfExe()+" memory-hook"))
			case "antigravity":
				reportInstall(e.name, antigravityHooksFile(), installJSONHook(antigravityHooksFile(), e.event, selfExe()+" memory-hook"))
			case "codex":
				// Not written: see the file header. A TOML config we cannot parse is not one we
				// should rewrite.
				fmt.Printf("● codex — paste into %s:\n\n%s\n", e.config, indentBlock(codexHookSnippet()))
			}
		}
		fmt.Println()
	}

	fmt.Printf("per-turn memory injection, for %s\n\n", dir)
	var warn []string
	for _, s := range memoryHookStatus(dir) {
		if !s.present {
			fmt.Printf("  %-12s not installed on this machine\n", s.engine)
			continue
		}
		state := "hook not installed"
		if s.installed {
			state = "hook installed, will it run? " + s.trusted.String()
		}
		fmt.Printf("  %-12s %s\n", s.engine, state)
		if s.detail != "" {
			fmt.Printf("  %-12s   %s\n", "", s.detail)
		}
		if s.installed && s.trusted != triYes {
			for _, e := range hookEngines() {
				if e.name == s.engine && e.trustDoc != "" {
					warn = append(warn, fmt.Sprintf("  %s: %s", s.engine, e.trustDoc))
				}
			}
		}
	}
	if len(warn) > 0 {
		fmt.Printf("\nnot yet guaranteed to run — grant trust once per engine:\n%s\n", strings.Join(warn, "\n"))
	}
	fmt.Println("\nthe memory map in AGENTS.md/CLAUDE.md/GEMINI.md works on every engine with no hook\nand no trust; the hook only adds mid-session refresh.")
}

func reportInstall(engine, path string, err error) {
	if err != nil {
		fmt.Printf("● %-12s %v\n", engine, err)
		return
	}
	fmt.Printf("● %-12s hook written to %s\n", engine, path)
}

func indentBlock(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}
