package engine

import (
	"fmt"
	"strings"
)

// ToolPosture is the tool budget a headless one-shot run is granted. Three
// postures cover every current caller: a tool-less reviewer that only reasons
// over the prompt text, a read-only explorer, and a build worker that edits
// files (Bash opt-in, per work.go invariant 4 — allowlist, never a bypass).
type ToolPosture struct {
	kind      postureKind
	allowBash bool
}

type postureKind int

const (
	postureNone     postureKind = iota // no tools at all (independent reviewer)
	postureReadOnly                    // Read/Grep/Glob-class inspection only
	postureWrite                       // read + edit + write; Bash only when allowBash
)

var (
	// ToolsNone: the run may only reason over the prompt — verify.go:172,
	// review_agent.go:144 (`--allowedTools ""`).
	ToolsNone = ToolPosture{kind: postureNone}
	// ToolsReadOnly: inspect the repo, change nothing — describe.go:247
	// (`--allowedTools "Read Grep Glob"`).
	ToolsReadOnly = ToolPosture{kind: postureReadOnly}
)

// ToolsWrite is the build-worker posture: read + edit + write files, plus Bash
// only when allowBash (work.go workerTools).
func ToolsWrite(allowBash bool) ToolPosture {
	return ToolPosture{kind: postureWrite, allowBash: allowBash}
}

func (p ToolPosture) String() string {
	switch p.kind {
	case postureNone:
		return "ToolsNone"
	case postureReadOnly:
		return "ToolsReadOnly"
	default:
		return fmt.Sprintf("ToolsWrite(allowBash=%v)", p.allowBash)
	}
}

// OneShotArgs builds the argv for a headless single-turn run at the given tool
// posture. argv[0] is the binary; when stdinPrompt is non-empty the prompt must
// be written to the process's stdin (and does NOT appear in argv), otherwise it
// is already embedded in argv. model is passed only when non-empty.
//
// An engine that cannot ENFORCE a posture non-interactively returns an error —
// never a silently-weaker invocation. What each engine can and cannot enforce
// (from the installed binaries' --help, 2026-07) is documented inline; the
// security slice builds on these notes.
func (s Spec) OneShotArgs(prompt, model string, posture ToolPosture) (argv []string, stdinPrompt string, err error) {
	switch s.Name {
	case "claude":
		return claudeOneShot(prompt, model, posture), "", nil
	case "codex":
		return codexOneShot(prompt, model, posture)
	case "gemini":
		return geminiOneShot(prompt, model, posture)
	case "opencode":
		return opencodeOneShot(prompt, model, posture), "", nil
	case "goose":
		argv, err = gooseOneShot(prompt, model, posture)
		return argv, "", err
	case "antigravity":
		// `agy --help`: the only non-interactive knobs are `-p/--print` (single
		// prompt, print the reply), `--sandbox` (terminal restrictions only) and
		// `--dangerously-skip-permissions` (auto-approve EVERYTHING). There is no
		// no-tools mode, no read-only mode, and no tool allowlist; a plain `-p`
		// run blocks on the first permission request until --print-timeout
		// (default 5m). Enforcing ANY posture would require the full bypass flag,
		// which violates the allowlist-not-bypass invariant (work.go) — so every
		// posture is refused rather than run weaker than asked.
		return nil, "", fmt.Errorf("antigravity: no non-interactive mode enforces %s — agy offers only --dangerously-skip-permissions (a full bypass), which partyline refuses", posture)
	default:
		return nil, "", fmt.Errorf("engine %q: one-shot invocation not defined", s.Name)
	}
}

// opencodeOneShot: `opencode run` with the prompt as the final positional. The POSTURE is not
// in argv — opencode enforces tool permissions from config, and OneShotEnv delivers a
// deny-by-default permission block per invocation via OPENCODE_CONFIG_CONTENT (verified against
// opencode 1.18.4: `opencode debug config` resolves the env-supplied block verbatim, and an
// explicit deny blocks the tool with no TTY involved). Every posture is enforceable — the first
// non-claude engine with no refused posture. `--auto` (a blanket auto-approve) is never passed:
// allowlist, not bypass (work.go invariant 4).
func opencodeOneShot(prompt, model string, posture ToolPosture) []string {
	a := []string{"opencode", "run"}
	if model != "" {
		a = append(a, "-m", model)
	}
	return append(a, prompt)
}

// OneShotEnv is the environment a one-shot run must carry for its posture to be ENFORCED, for
// engines whose tool controls live in config rather than argv. opencode reads
// OPENCODE_CONFIG_CONTENT (inline JSON, no file lifecycle) and applies the permission block at
// the tool layer: "*" deny first, explicit allows after. Other engines need nothing → nil.
// Permission keys per the opencode docs (read, edit, glob, grep, bash, task, skill, lsp,
// question, webfetch, websearch, external_directory, doom_loop) — everything not allowed is
// denied by the wildcard, so a NEW opencode tool ships denied here until we allow it.
func (s Spec) OneShotEnv(posture ToolPosture) []string {
	switch s.Name {
	case "opencode":
		perm := `{"*":"deny"}` // ToolsNone: reason over the prompt only
		switch posture.kind {
		case postureReadOnly:
			perm = `{"*":"deny","read":"allow","grep":"allow","glob":"allow"}`
		case postureWrite:
			if posture.allowBash {
				perm = `{"*":"deny","read":"allow","edit":"allow","grep":"allow","glob":"allow","bash":"allow"}`
			} else {
				perm = `{"*":"deny","read":"allow","edit":"allow","grep":"allow","glob":"allow"}`
			}
		}
		return []string{`OPENCODE_CONFIG_CONTENT={"permission":` + perm + `}`}
	case "goose":
		// goose's per-invocation posture lever is GOOSE_MODE (a session column, honored from env):
		// chat = the model may reason and answer but NO tool call executes (the reviewer posture);
		// auto = full autonomy, tools execute without approval (the build-worker posture). ReadOnly
		// and no-bash-write never reach here — gooseOneShot refuses them — so only None/Write map.
		if posture.kind == postureWrite {
			return []string{"GOOSE_MODE=auto"}
		}
		return []string{"GOOSE_MODE=chat"}
	default:
		return nil
	}
}

// gooseOneShot builds `goose run` for a headless one-shot. Prompt is the value of -t; -q +
// --output-format text yield the clean model reply (the caller's own parse reads it); --no-session
// keeps automated runs out of the interactive session store. The POSTURE is enforced by GOOSE_MODE
// (OneShotEnv), not argv — but two postures are REFUSED here because goose can't enforce them
// headless, and running wider than asked is the invariant we never break:
//   - read-only — GOOSE_MODE has no such value. chat can't read the repo (no tools at all); auto
//     writes; approve blocks on the first prompt with no TTY. None is a real read-only posture, so
//     rather than silently pick one, refuse.
//   - write-without-bash — goose's file edits and shell both live in the `developer` extension;
//     there's no headless knob to grant edit while denying shell, so a no-bash write can't be
//     honored. (Mirrors codex, whose sandbox is likewise all-or-nothing here.)
func gooseOneShot(prompt, model string, posture ToolPosture) ([]string, error) {
	switch posture.kind {
	case postureReadOnly:
		return nil, fmt.Errorf("goose: no headless read-only mode — GOOSE_MODE is chat (no tools) or auto (full autonomy), neither reads without writing; refused rather than run wider than asked")
	case postureWrite:
		if !posture.allowBash {
			return nil, fmt.Errorf("goose: can't grant file edits without shell — its developer extension bundles edit+shell, so a no-bash write posture isn't enforceable; refused")
		}
	}
	a := []string{"goose", "run", "-t", prompt, "--output-format", "text", "-q", "--no-session"}
	if model != "" {
		a = append(a, "--model", model)
	}
	return a, nil
}

// claudeOneShot mirrors the exact flags the hardcoded call sites use today:
// `-p <prompt> --output-format json --allowedTools <list>` (+ --model), where
// list is "" (verify.go:172, review_agent.go:144), "Read Grep Glob"
// (describe.go:247), or the comma-joined workerTools set (work.go). Claude
// enforces the allowlist per tool — the strongest posture control of the four
// engines — and a headless -p run silently denies any tool not listed.
func claudeOneShot(prompt, model string, posture ToolPosture) []string {
	tools := ""
	switch posture.kind {
	case postureReadOnly:
		tools = "Read Grep Glob"
	case postureWrite:
		list := []string{"Read", "Grep", "Glob", "Edit", "Write", "MultiEdit", "TodoWrite"}
		if posture.allowBash {
			list = append(list, "Bash")
		}
		tools = strings.Join(list, ",")
	}
	argv := []string{"claude", "-p", prompt, "--output-format", "json", "--allowedTools", tools}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return argv
}

// codexOneShot: `codex exec --help` — non-interactive by design, prompt on
// stdin. Posture control is the OS-level sandbox: `--sandbox
// <read-only|workspace-write|danger-full-access>`. That is kernel enforcement
// (stronger than an allowlist) but coarser: codex has no tool-less mode, and no
// way to allow file edits while denying shell (its edits ride the same sandboxed
// exec pipeline). Also relevant for the security slice: `--skip-git-repo-check`
// (codex refuses to run outside a git repo without it), `-C/--cd <dir>`,
// `--add-dir <dir>` (extra writable roots), `--json` (JSONL events),
// `-o/--output-last-message <file>`, and the `--dangerously-bypass-approvals-
// and-sandbox` flag partyline must never emit.
func codexOneShot(prompt, model string, posture ToolPosture) ([]string, string, error) {
	sandbox := ""
	switch posture.kind {
	case postureNone:
		// Even `--sandbox read-only` still reads the repo and runs commands —
		// weaker than "no tools", so refuse.
		return nil, "", fmt.Errorf("codex: no non-interactive mode enforces ToolsNone — codex exec cannot disable its tool loop (nearest is --sandbox read-only, which still reads files and runs commands)")
	case postureReadOnly:
		sandbox = "read-only"
	case postureWrite:
		if !posture.allowBash {
			// workspace-write permits writes via model-generated COMMANDS; there
			// is no "edit files but no shell" boundary to enforce.
			return nil, "", fmt.Errorf("codex: no non-interactive mode enforces ToolsWrite(allowBash=false) — codex applies edits through its sandboxed exec pipeline and cannot separate file edits from shell; use ToolsWrite(true) (--sandbox workspace-write)")
		}
		sandbox = "workspace-write"
	}
	argv := []string{"codex", "exec"}
	if model != "" {
		argv = append(argv, "-m", model)
	}
	return append(argv, "--sandbox", sandbox), prompt, nil
}

// geminiOneShot: `gemini --help` — headless mode is `-p/--prompt <prompt>`.
//
// Verified 2026-07-14 (gemini 0.45.2): in an UNTRUSTED folder gemini overrides the approval
// mode to "default" and refuses headless tool use outright — fail-closed, but also
// non-functional. Users must trust the project dir (interactive `gemini` once, or
// GEMINI_CLI_TRUST_WORKSPACE=true) before gemini agents work under the daemon.
// Posture control is `--approval-mode <default|auto_edit|yolo|plan>`: plan =
// read-only mode, auto_edit = auto-approve edit tools only, yolo = auto-approve
// everything. In headless mode a tool call the approval mode doesn't cover
// cannot be approved, so it fails rather than escalates. Caveats for the
// security slice: yolo is broader than claude's write allowlist (it also
// approves web/MCP tools); `--allowed-tools` exists but is DEPRECATED (policy
// engine replaces it) so we don't build on it; `-s/--sandbox` (boolean,
// container-based) and `-o/--output-format text|json|stream-json` exist but are
// not wired here. There is no tool-less mode.
func geminiOneShot(prompt, model string, posture ToolPosture) ([]string, string, error) {
	mode := ""
	switch posture.kind {
	case postureNone:
		return nil, "", fmt.Errorf("gemini: no non-interactive mode enforces ToolsNone — the nearest posture is --approval-mode plan (read-only), which still reads the workspace")
	case postureReadOnly:
		mode = "plan"
	case postureWrite:
		mode = "auto_edit"
		if posture.allowBash {
			mode = "yolo" // approves ALL tools, not just shell — see caveat above
		}
	}
	argv := []string{"gemini"}
	if model != "" {
		argv = append(argv, "-m", model)
	}
	return append(argv, "--approval-mode", mode, "-p", prompt), "", nil
}
