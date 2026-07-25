// scribe.go — Mode-4 context capture (docs/plans/context-mode4-capture.md).
//
// Every LLM session in partyline — the human mux orchestrator, a crank coder, a review grader,
// a solo user — writes its reasoning to an append-only engine jsonl and NOWHERE shared. This
// distills that jsonl into durable Context-Thread facts (context_blocks) so a session's
// decisions/constraints/contracts survive compaction+restart and are visible to peers on the
// same thread (the substance behind `ask_peers`).
//
// Design invariants:
//   - The jsonl is the source of truth. It is append-only ACROSS compaction (a `/compact`
//     rewrites the live window, never the on-disk log), so a watermark-forward read can never
//     miss a turn — no compaction-boundary race to win.
//   - Runs LOCALLY on the daemon, over the session's OWN engine. Only distilled facts leave the
//     machine; the raw transcript never does (right for a private pairing session, and the only
//     mechanism that generalizes to the factory + solo users with no server config).
//   - Reuse, don't reinvent: the rubric echoes seed_from_history / distill.ts; facts are ordinary
//     context_blocks written via Remember(); dedup is the model's job via recall-first + supersede.
//
// The pure pieces (slice read, transcript render, output parse) are split out for unit tests; the
// engine exec + Remember writes are the thin IO shell (runScribePass).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

const (
	// scribeMaxLine skips embedded tool-output blobs — same bound scanClaude uses so one giant
	// line can't blow the read up.
	scribeMaxLine = 256 << 10
	// scribeMaxTranscript caps what we feed the engine in one pass. The slice is only NEW turns
	// since the watermark, but a long uninterrupted stretch can still be large; keep the most
	// recent tail when it overflows (oldest context is likeliest already captured by a prior pass).
	scribeMaxTranscript = 200 << 10
)

// scribeMaxRead bounds how many bytes ONE pass pulls into memory. A first pass (watermark 0) over a
// huge session file would otherwise read the whole thing (100s of MB) only for renderTranscript to
// keep the 200KB tail — so cap the read window and skip ahead to the tail when the gap is bigger.
// A var, not const, so a test can shrink it. Incremental passes have small gaps and never hit this.
var scribeMaxRead int64 = 4 << 20

// scribeTurn is one distilled transcript turn — role ("user"/"assistant") + its text, everything
// else (tokens, model, timestamps, tool calls) dropped.
type scribeTurn struct {
	Role string
	Text string
}

// readTranscriptSlice reads NEW complete lines of an append-only session jsonl from fromOffset to
// EOF, parsing each into a role+text turn via the engine-specific lineMsg. It returns the turns and
// the new watermark — the byte offset AFTER the last COMPLETE line (a line still being written at
// EOF is left for the next pass, so we never parse a half-flushed record). A file that has not grown
// a full line since fromOffset yields no turns and an unchanged offset.
func readTranscriptSlice(path string, fromOffset int64, lm lineMsg) (turns []scribeTurn, newOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fromOffset, err
	}
	size := fi.Size()
	if fromOffset >= size {
		return nil, fromOffset, nil // nothing new (append-only, so fromOffset can't legitimately exceed size)
	}
	// Bound the read window: on a big gap, skip ahead to the last scribeMaxRead bytes — the older turns
	// would be dropped by renderTranscript's tail cap anyway, and this keeps memory flat on a first pass.
	origin := fromOffset
	skippedHead := false
	if size-origin > scribeMaxRead {
		origin = size - scribeMaxRead
		skippedHead = true
	}
	if origin > 0 {
		if _, err = f.Seek(origin, io.SeekStart); err != nil {
			return nil, fromOffset, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fromOffset, err
	}
	// A forward skip lands mid-line — drop the partial head so we never parse a fragment.
	if skippedHead {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, size, nil // one giant line filled the window — skip it, watermark to EOF
		}
		origin += int64(i + 1)
		data = data[i+1:]
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return nil, fromOffset, nil // no complete line yet
	}
	newOffset = origin + int64(lastNL+1)
	for _, line := range bytes.Split(data[:lastNL+1], []byte{'\n'}) {
		if len(line) == 0 || len(line) > scribeMaxLine {
			continue
		}
		role, text, _, _, _, _ := lm(line)
		if (role != "user" && role != "assistant") || strings.TrimSpace(text) == "" {
			continue
		}
		turns = append(turns, scribeTurn{Role: role, Text: strings.TrimSpace(text)})
	}
	return turns, newOffset, nil
}

// renderTranscript flattens turns into a plain "USER:/ASSISTANT:" transcript for the engine,
// bounded to maxBytes by dropping the OLDEST turns (the recent tail carries the decisions a
// checkpoint most needs to capture). Returns "" when there is nothing to distill.
func renderTranscript(turns []scribeTurn, maxBytes int) string {
	var b strings.Builder
	for _, t := range turns {
		label := "USER"
		if t.Role == "assistant" {
			label = "ASSISTANT"
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(t.Text)
		b.WriteString("\n\n")
	}
	s := b.String()
	if len(s) > maxBytes { // keep the tail
		s = "…(earlier turns elided)…\n\n" + s[len(s)-maxBytes:]
	}
	return strings.TrimSpace(s)
}

// scribeFact is one parsed fact from the engine's distillation turn. Supersedes (optional) points at
// an existing block id the model chose to update — the dedup/convergence lever (recall-first tells
// it what's already recorded).
type scribeFact struct {
	Kind       string   `json:"kind"`
	Body       string   `json:"body"`
	Entities   []string `json:"entities,omitempty"`
	Supersedes int64    `json:"supersedes,omitempty"`
}

var scribeKinds = map[string]bool{"decision": true, "constraint": true, "contract": true, "question": true, "overview": true}

// parseScribeFacts extracts the engine's JSON array of facts, tolerating prose or a fenced ```json
// block around it (same shape tolerance as describe's parseReqTree). Unknown-kind or empty-body
// items are dropped rather than failing the whole pass — a partial distill is still worth writing.
func parseScribeFacts(reply string) ([]scribeFact, error) {
	raw := strings.TrimSpace(reply)
	if m := jsonBlockRe.FindStringSubmatch(reply); m != nil {
		raw = strings.TrimSpace(m[1])
	} else if i := strings.IndexByte(raw, '['); i >= 0 {
		if j := strings.LastIndexByte(raw, ']'); j > i {
			raw = raw[i : j+1]
		}
	}
	var facts []scribeFact
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return nil, fmt.Errorf("scribe: unparseable distill output: %w", err)
	}
	out := facts[:0]
	for _, f := range facts {
		f.Kind = strings.TrimSpace(strings.ToLower(f.Kind))
		f.Body = strings.TrimSpace(f.Body)
		if !scribeKinds[f.Kind] || f.Body == "" {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// buildScribePrompt assembles the distillation turn: the rubric, the facts already recorded (so the
// model dedups by recall-first + supersede rather than piling on), and the new transcript slice.
func buildScribePrompt(existing []api.ContextBlock, transcript string) string {
	var b strings.Builder
	b.WriteString(scribeRubric)
	b.WriteString("\n\nEXISTING FACTS (already on the thread — do NOT repeat; `supersedes` the id of any you are updating):\n")
	if len(existing) == 0 {
		b.WriteString("(none yet)\n")
	}
	for _, e := range existing {
		fmt.Fprintf(&b, "- [%d] (%s) %s\n", e.ID, e.Kind, e.Body)
	}
	b.WriteString("\nCONVERSATION SLICE to distill:\n\n")
	b.WriteString(transcript)
	return b.String()
}

const scribeRubric = `You are the context scribe. Extract the DURABLE, CROSS-BOUNDARY facts from this conversation slice — the "seam" context another person or component will depend on: decisions made, hard constraints hit, API/interface contracts, and open questions that block others.

STRICT RULES:
- Only facts that cross the boundary between people or components. NOT one person's local working notes, chit-chat, mechanical steps, or transient status.
- Prefer FEWER, higher-value facts. When in doubt, SKIP IT. Over-capture is worse than under-capture.
- Do NOT repeat anything in "EXISTING FACTS". If a slice UPDATES an existing fact, set "supersedes" to its id instead of writing a duplicate.
- No secrets, keys, tokens, passwords, or personal data.
- Capture DURABLE facts about the WORK (decisions, constraints, contracts), NOT the momentary state of this session's process. Skip "X hasn't run yet", "as of now", "the next step is…" — those go stale immediately and are not seam facts.
- One or two sentences each; concrete and self-contained.
- Tag each with 1-3 "entities": short slugs naming what the fact is about (the service/API/component), reusing slugs already implied by EXISTING FACTS where they fit.

Return ONLY a JSON array (possibly empty), each item:
{"kind":"decision|constraint|contract|question","body":"...","entities":["slug"],"supersedes":<id or omit>}
No prose, no code fences.`

// ---- local watermark: per-session processing state, daemon-local (no server table) -------------
//
// How far the local daemon has distilled a local jsonl is pure per-machine state, so it lives in a
// file, not the DB. Losing it (reinstall) just means a re-distill — recall-first + supersede dedups.

type scribeState struct {
	ThreadID string `json:"thread_id"`
	Offset   int64  `json:"offset"`
	Updated  string `json:"updated"`
}

func scribeStatePath(sessionID string) string {
	return filepath.Join(stateDir(), "daemon", "scribe", sanitizeSessionID(sessionID)+".json")
}

// sanitizeSessionID keeps the watermark filename to a safe basename (session ids are engine-issued
// uuids, but never trust an id straight into a path).
func sanitizeSessionID(id string) string {
	id = filepath.Base(id)
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func readScribeState(sessionID string) scribeState {
	var s scribeState
	b, err := os.ReadFile(scribeStatePath(sessionID))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func writeScribeState(sessionID string, s scribeState) error {
	p := scribeStatePath(sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

// ---- the pass: read slice → distill → write blocks → advance watermark -------------------------

// lineMsgFor picks the per-line parser for a session tool ("" if unsupported).
func lineMsgFor(tool string) lineMsg {
	switch tool {
	case "claude":
		return claudeLineMsg
	case "codex":
		return codexLineMsg
	default:
		return nil
	}
}

// runScribePass distills everything new in one session's jsonl into thread facts, then advances the
// local watermark. Best-effort and idempotent-ish: a failed engine pass leaves the watermark
// untouched so the next pass retries the same slice. minTurns is the cost guard — below it we skip
// the (real, engine-costing) pass entirely.
func runScribePass(client *api.Client, threadID, tool, engineName, model, sessionID, storePath string, minTurns int, logf func(string)) (written int, err error) {
	if threadID == "" || storePath == "" {
		return 0, nil // nothing to write to / nothing to read
	}
	lm := lineMsgFor(tool)
	if lm == nil {
		return 0, nil // engine we can't parse — skip silently
	}
	st := readScribeState(sessionID)
	if st.ThreadID != "" && st.ThreadID != threadID {
		st.Offset = 0 // thread was re-attached — re-baseline against the new thread
	}
	turns, newOffset, err := readTranscriptSlice(storePath, st.Offset, lm)
	if err != nil {
		return 0, err
	}
	if len(turns) < minTurns {
		return 0, nil // cost guard: not enough new conversation to be worth an engine pass
	}
	transcript := renderTranscript(turns, scribeMaxTranscript)
	if transcript == "" {
		return 0, nil
	}
	existing, _ := client.Recall(threadID, 0) // recall-first: the model dedups/supersedes against this
	spec, ok := engineSpecFor(engineName)
	if !ok {
		return 0, fmt.Errorf("scribe: unknown engine %q", engineName)
	}
	argv, stdinPrompt, err := reviewerOneShot(spec, buildScribePrompt(existing, transcript), model, logf) // tool-less: distilling needs no tools
	if err != nil {
		return 0, fmt.Errorf("scribe: build argv: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := runOneShot(ctx, os.TempDir(), argv, stdinPrompt)
	if err != nil {
		return 0, fmt.Errorf("scribe: engine: %w", err)
	}
	facts, err := parseScribeFacts(oneShotText(spec, out))
	if err != nil {
		return 0, err
	}
	author := "scribe:" + engineLabel(engineName)
	for _, f := range facts {
		if _, werr := client.Remember(threadID, f.Kind, f.Body, author, engineLabel(engineName), f.Supersedes, f.Entities); werr != nil {
			if logf != nil {
				logf(fmt.Sprintf("scribe: remember failed (%v)", werr))
			}
			continue // a single write failing shouldn't strand the rest or the watermark
		}
		written++
	}
	// Advance the watermark only after a successful pass — a mid-pass failure above returned early
	// with the offset untouched, so the same slice is retried next time.
	_ = writeScribeState(sessionID, scribeState{ThreadID: threadID, Offset: newOffset, Updated: time.Now().UTC().Format(time.RFC3339)})
	return written, nil
}
