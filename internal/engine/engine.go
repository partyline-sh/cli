// Package engine is the single adapter for invoking agent CLIs (claude, codex,
// gemini, antigravity). It owns, per engine: how to build an argv (interactive
// party turns via Spec.Args, headless one-shots via Spec.OneShotArgs), how the
// prompt is delivered (stdin vs final arg), whether the output is a parseable
// stream, and how to read the final result (Spec.ParseResult). Everything in
// package main that execs an agent CLI goes through here — adding an engine is
// one entry in the specs map below.
package engine

// Caps declares what partyline can rely on an engine to do. Each flag gates a
// concrete behavior in package main — the file:line notes say which one.
type Caps struct {
	// Resume: the engine can continue a prior headless session from an opaque
	// session id. Gates party embodiment (party_agent.go:536-539, claude
	// `--resume <id> --fork-session`), worker resume (work.go runWorker's
	// `--resume` arg), and interview turns (describe.go:248-250).
	// codex `exec resume <id>` and gemini `-r/--resume` exist per their --help,
	// but no partyline path drives them yet — false until wired and verified.
	Resume bool
	// Stream: the engine emits NDJSON events we parse for live tool-call
	// activity. Gates the streaming paths in runAgentPlain
	// (party_agent.go:1035+) and runWorkerStreaming (worker_stream.go) —
	// claude `--output-format stream-json --verbose`. gemini advertises
	// `-o stream-json` per --help but its event shape is unverified — false.
	Stream bool
	// Vision: a headless run can OPEN image files and judge them (claude's
	// Read tool renders images). Gates the visual reviewer (visual.go:190-205).
	Vision bool
	// MCPPerInvocation: an MCP server can be registered for a single
	// invocation via argv, without touching the user's config. Gates party
	// MCP wiring (party_agent.go mcpArgsFor: claude `--mcp-config <json>`
	// at :904-906, codex `-c mcp_servers.*` TOML overrides at :908-914).
	// gemini/antigravity have no per-invocation MCP flag (see the mcpActive
	// note at party_agent.go:875) — false.
	MCPPerInvocation bool
}

// Spec is how to invoke one agent CLI: the binary, how the prompt is delivered
// (Stdin=true → on stdin, else appended as the final arg), whether its output
// is a parseable event stream (claude stream-json only), and the args builder
// for an interactive/party turn. Headless one-shots use OneShotArgs (oneshot.go);
// final-result parsing is ParseResult (result.go).
type Spec struct {
	Name   string
	Bin    string
	Stdin  bool // prompt on stdin (else final argv element)
	Stream bool // stdout is claude-style stream-json we can parse live
	// Args builds the CLI flags for a party turn from the model + the
	// operator's native passthrough (e.g. --permission-mode bypassPermissions).
	// Each engine places passthrough where its parser expects flags — before
	// the prompt-delivering arg. model is passed only when non-empty.
	Args func(model string, extra []string) []string
	Caps Caps
}

// specs is the engine registry. The Args builders are ported verbatim from the
// old party map (party_agent.go) — party argv output must stay byte-identical.
var specs = map[string]Spec{
	// Claude Code: stream-json gives us the live tool-call activity view.
	// Prompt on stdin, so passthrough can go at the end.
	"claude": {Name: "claude", Bin: "claude", Stdin: true, Stream: true,
		Caps: Caps{Resume: true, Stream: true, Vision: true, MCPPerInvocation: true},
		Args: func(m string, extra []string) []string {
			a := []string{"-p", "--output-format", "stream-json", "--verbose"}
			if m != "" {
				a = append(a, "--model", m)
			}
			return append(a, extra...)
		}},
	// OpenAI Codex: `codex exec` reads the prompt from stdin; plain output for now.
	"codex": {Name: "codex", Bin: "codex", Stdin: true, Stream: false,
		Caps: Caps{MCPPerInvocation: true},
		Args: func(m string, extra []string) []string {
			a := []string{"exec"}
			if m != "" {
				a = append(a, "-m", m)
			}
			return append(a, extra...)
		}},
	// Gemini CLI: `gemini -p <prompt>` — prompt is the final arg, so passthrough
	// must come BEFORE -p.
	"gemini": {Name: "gemini", Bin: "gemini", Stdin: false, Stream: false,
		Caps: Caps{},
		Args: func(m string, extra []string) []string {
			a := []string{}
			if m != "" {
				a = append(a, "-m", m)
			}
			a = append(a, extra...)
			return append(a, "-p")
		}},
	// opencode: `opencode run [message..]` — prompt is the final positional, so passthrough
	// goes before it. Model is `provider/model` (e.g. moonshot/kimi-k3, ollama/qwen3) — THE
	// open-weight gateway: any OpenAI-compatible endpoint opencode is configured for. Plain
	// output for now (`--format json` emits raw events; unverified shape → not Stream).
	// `-s <id>` resume exists per --help but no partyline path drives it yet — Resume false
	// until wired and verified, same posture as codex `exec resume`.
	"opencode": {Name: "opencode", Bin: "opencode", Stdin: false, Stream: false,
		Caps: Caps{},
		Args: func(m string, extra []string) []string {
			a := []string{"run"}
			if m != "" {
				a = append(a, "-m", m)
			}
			return append(a, extra...)
		}},
	// Google Antigravity: `agy -p <prompt>` runs one prompt non-interactively and
	// prints the reply (prompt is the final arg, like gemini). Plain text out.
	// MCP/tool-approval path isn't wired (text-in/out only, as with gemini).
	"antigravity": {Name: "antigravity", Bin: "agy", Stdin: false, Stream: false,
		Caps: Caps{},
		Args: func(m string, extra []string) []string {
			a := []string{}
			if m != "" {
				a = append(a, "--model", m)
			}
			a = append(a, extra...)
			return append(a, "-p")
		}},
}

// Lookup returns the Spec for a canonical engine name ("claude", "codex",
// "gemini", "antigravity"). It does not resolve aliases — callers that accept
// "agy" (the antigravity binary name) normalize before calling.
func Lookup(name string) (Spec, bool) {
	s, ok := specs[name]
	return s, ok
}

// Valid reports whether name is a known engine.
func Valid(name string) bool { _, ok := specs[name]; return ok }

// Label is the display name for an engine setting ("" → "claude", the default).
func Label(name string) string {
	if name == "" {
		return "claude"
	}
	return name
}

// Names lists the engines in stable presentation order.
func Names() []string { return []string{"claude", "codex", "gemini", "opencode", "antigravity"} }
