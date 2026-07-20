package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	eng "partyline.sh/partyline/internal/engine"
)

// worker_stream.go — crank-01, LIVE STEP OUTPUT. The streaming half of runWorker: when a live log sink
// is wired, the worker runs claude with `--output-format stream-json` (NDJSON — one event per line,
// emitted as the agent works) instead of the buffered `--output-format json`. We read stdout line by
// line, HUMANIZE each event into a concise step line for onLog (so the run detail page tails it like
// GitHub Actions step logs), and capture the terminal `result` event for the final answer text + token
// usage (keeping the O.5 token ceiling accounting intact). Every raw line still becomes at least one log
// line, so even an unrecognized event shape is surfaced rather than swallowed.

const maxLogLine = 2000 // one streamed step line; a chatty tool result is truncated, not stored whole

// rateLimitReset is the engine-neutral "the model provider throttled us" signal: a non-zero time is
// when the quota window resets (so the run can resume then). Zero = no rate-limit hit. Detection is
// per-engine (see parseRateLimit — Claude's stream `rate_limit_event` today); other engines fill in
// their own signal (429 / stderr / exit) as adapters land. Kept a plain time so the whole pipeline
// (crank → daemon → run status → web) stays engine-agnostic.
type rateLimitReset = time.Time

// runWorkerStreaming drives the worker in stream-json mode. It returns a workerOutcome (final text,
// tokens, the engine's resume handle, and a rate-limit reset time — zero if none) so the caller can
// pause-and-resume instead of reporting a bare failure.
func runWorkerStreaming(ctx context.Context, cmd *exec.Cmd, timeout time.Duration, onLog func(string)) (workerOutcome, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return workerOutcome{}, err
	}
	// stderr is not part of the event stream (claude logs diagnostics there); surface it as-is so a
	// crash reason isn't lost, but keep it out of the structured result parse.
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return workerOutcome{}, err
	}

	var (
		resultText   string
		resultTokens int
		resumeHandle string // engine session id — captured from the FIRST event that carries one (see below)
		sawResult    bool
		rlReset      time.Time // quota-reset time from a blocking rate_limit_event (zero = none given)
		rlBlocked    bool      // the provider REFUSED — true even with no reset (entitlement/credits blocks carry none)
		rlNote       string    // the provider's own wording for the block, when it gave us one
	)
	// Skill-invocation telemetry: watch the tool-use stream for an injected skill being ACTIVATED or
	// its files touched, so the library can show "invoked in M runs" (not just "injected"). Empty set
	// (ptln work / describe, or a run with no org skills) → the watcher is a no-op.
	watch := newSkillWatch(workerSkillNames)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20) // agent messages + tool results can be large
	for sc.Scan() {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		watch.inspect(raw)
		// Capture the resume handle from the FIRST event that carries a session id (Claude stamps it
		// on the init `system` frame, before any result). Doing it early — not just from the terminal
		// `result` event — means an INTERRUPTED run (timeout, rate limit, kill) still has a handle to
		// resume from, which is exactly the case resume exists for.
		if resumeHandle == "" {
			if id := parseSessionID(raw); id != "" {
				resumeHandle = id
			}
		}
		if reset, blocked, note := parseRateLimit(raw); blocked {
			rlBlocked = true
			if note != "" {
				rlNote = note
			}
			rlReset = reset
			switch {
			case !reset.IsZero():
				onLog("⏸ rate limit reached — resets " + reset.Format("Jan 2 3:04 PM"))
			case rlNote != "":
				onLog("⏸ blocked by the provider — " + rlNote)
			default:
				// No reset and no message: say what we actually know rather than inventing a window.
				onLog("⏸ blocked by the provider (no reset time — usually a credits/entitlement limit)")
			}
		}
		lines, res := humanizeStreamEvent(raw)
		for _, l := range lines {
			if l = strings.TrimSpace(l); l != "" {
				onLog(clampLog(l))
			}
		}
		if res != nil {
			resultText, resultTokens, sawResult = res.Text, res.Usage.Total(), true
			if res.SessionID != "" {
				resumeHandle = res.SessionID // the result event's id is authoritative if present
			}
		}
	}
	waitErr := cmd.Wait()

	// Whatever skills the agent invoked before the stream ended — reported even on a timeout/crash, since
	// a skill used before the interruption was still genuinely used.
	invoked := watch.invoked()

	if ctx.Err() == context.DeadlineExceeded {
		onLog("■ hit the time budget — stopped")
		fallback := resultText
		if fallback == "" {
			fallback = stderr.String()
		}
		// Keep the resume handle on a timeout — the whole point is to continue from where it stopped.
		return workerOutcome{text: fallback, tokens: resultTokens, resumeHandle: resumeHandle, rateReset: rlReset, rateBlocked: rlBlocked, rateNote: rlNote, invokedSkills: invoked}, fmt.Errorf("hit the %s time budget — stopped", timeout)
	}
	if sawResult {
		return workerOutcome{text: resultText, tokens: resultTokens, resumeHandle: resumeHandle, rateReset: rlReset, rateBlocked: rlBlocked, rateNote: rlNote, invokedSkills: invoked}, waitErr
	}
	// No structured result event (unexpected output / a non-claude engine later): degrade like the
	// buffered path — unknown (0) usage, raw stderr as the text — but keep any handle/reset we saw.
	return workerOutcome{text: stderr.String(), resumeHandle: resumeHandle, rateReset: rlReset, rateBlocked: rlBlocked, rateNote: rlNote, invokedSkills: invoked}, waitErr
}

// parseSessionID is the engine seam for "this run's resume handle." Claude Code stamps a session_id
// on every stream event (the init system frame onward); we capture the first one so an interrupted
// run can be resumed even when no terminal result event arrives. Empty when the line carries none
// (or the engine doesn't support resume) — the caller then falls back to restart-from-start.
func parseSessionID(raw []byte) string {
	var e struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return e.SessionID
}

// parseRateLimit is the ENGINE SEAM for "the provider throttled us." Today it reads Claude Code's
// stream `rate_limit_event`; blocked=true (with the window's reset time) when there's no headroom —
// the request was/would be rejected and no overage is available. Other engines add their own signal
// here (HTTP 429, a stderr marker, an exit code) as adapters land; the rest of the pipeline only
// sees the neutral (resetAt, blocked) result. Non-rate-limit lines return (zero, false).
func parseRateLimit(raw []byte) (resetAt time.Time, blocked bool, note string) {
	var e struct {
		Type string `json:"type"`
		Info struct {
			Status        string `json:"status"`
			OverageStatus string `json:"overageStatus"`
			ResetsAt      int64  `json:"resetsAt"`
			// Best-effort human reason. Providers word entitlement blocks differently and the field
			// name isn't guaranteed, so several are read tolerantly — an absent one just unmarshals
			// empty and we fall back to our own wording. Never invent a schema we haven't seen.
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"rate_limit_info"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Type != "rate_limit_event" {
		return time.Time{}, false, ""
	}
	// Only an ACTUAL block pauses the run — status rejected/exhausted/blocked. A "status:allowed"
	// event (even with overageStatus:rejected, i.e. "no overage left" — exactly the event we saw)
	// means the request went through, so it must NOT pause a healthy run. Under-triggering is safe
	// (the run just falls back to normal failure handling); over-triggering would halt good runs.
	blockedStatus := e.Info.Status == "rejected" || e.Info.Status == "exhausted" || e.Info.Status == "blocked"
	if !blockedStatus {
		return time.Time{}, false, ""
	}
	for _, m := range []string{e.Info.Message, e.Info.Reason, e.Message} {
		if m = strings.TrimSpace(m); m != "" {
			note = m
			break
		}
	}
	// A BLOCK WITH NO RESET TIME IS STILL A BLOCK. This used to require ResetsAt > 0, which quietly
	// discarded the entire class of ENTITLEMENT blocks — "usage credits are required for this
	// model", org-level model disabled — because those aren't time-windowed and carry no reset. The
	// run then died as a bare `exit status 1` with the real reason nowhere on screen, which is how a
	// 2.7M-token run looked like a mystery crash instead of "this model needs credits".
	//
	// Reporting a blocked-with-no-reset is safe in the direction that matters: it pauses the run for
	// a human instead of failing it silently, and SetRunPaused simply omits resume_at when the reset
	// is zero, so the web offers "resume" rather than a bogus "resume at 1970".
	if e.Info.ResetsAt > 0 {
		return time.Unix(e.Info.ResetsAt, 0), true, note
	}
	return time.Time{}, true, note
}

func clampLog(s string) string {
	if len(s) > maxLogLine {
		return s[:maxLogLine] + "…"
	}
	return s
}

// workerStreamEvent is the subset of claude's stream-json events we read. The stream is a sequence of these,
// one per line: system (init/…), assistant (the agent's message — text + tool_use blocks), user (tool
// results), and a terminal result (final answer + usage). We stay tolerant: unknown fields are ignored
// and an unrecognized shape falls back to the compact raw line.
type workerStreamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	Usage     eng.Usage       `json:"usage"`
	SessionID string          `json:"session_id"` // Slice 2: resume handle (present on the terminal result event too)
}

type workerStreamMessage struct {
	Content []workerStreamBlock `json:"content"`
}

type workerStreamBlock struct {
	Type  string          `json:"type"` // "text" | "tool_use" | "tool_result"
	Text  string          `json:"text"`
	Name  string          `json:"name"`  // tool_use: the tool name
	Input json.RawMessage `json:"input"` // tool_use: the tool args
}

// humanizeStreamEvent turns one raw NDJSON event into human-readable step line(s) and, for the terminal
// result event, the parsed engine.Result (text + usage). It never returns zero lines for a non-result
// event — an unrecognized shape yields the trimmed raw line so nothing is silently dropped.
func humanizeStreamEvent(raw []byte) (lines []string, result *eng.Result) {
	var ev workerStreamEvent
	if json.Unmarshal(raw, &ev) != nil {
		return []string{string(raw)}, nil
	}
	switch ev.Type {
	case "system":
		// init and other system frames are noise for a human tail; skip unless it's a subtype worth
		// a one-liner. Keep the stream readable.
		if ev.Subtype != "" && ev.Subtype != "init" {
			return []string{"· " + ev.Subtype}, nil
		}
		return nil, nil
	case "result":
		r := &eng.Result{Text: ev.Result, Usage: ev.Usage, SessionID: ev.SessionID}
		return nil, r
	case "assistant":
		return assistantLines(ev.Message), nil
	case "user":
		return toolResultLines(ev.Message), nil
	case "rate_limit_event":
		// NOT a human log line. A genuine BLOCK is already narrated by the parseRateLimit path above
		// ("⏸ rate limit reached…"); an ALLOWED event (status:allowed, just reporting overage
		// headroom) is pure noise — and dumping its raw JSON as the step line is exactly what made an
		// allowed run look blocked ("overageDisabledReason":"org_level_disabled" screamed on a card
		// whose request had actually gone through). Drop it here so it never reaches the tail.
		return nil, nil
	default:
		return []string{string(raw)}, nil
	}
}

// assistantLines renders the agent's message: prose text lines, and a "→ Tool(args)" line per tool call.
func assistantLines(rawMsg json.RawMessage) []string {
	var m workerStreamMessage
	if len(rawMsg) == 0 || json.Unmarshal(rawMsg, &m) != nil {
		return nil
	}
	var out []string
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			for _, ln := range strings.Split(strings.TrimSpace(b.Text), "\n") {
				if ln = strings.TrimSpace(ln); ln != "" {
					out = append(out, ln)
				}
			}
		case "tool_use":
			out = append(out, "→ "+b.Name+toolArgHint(b.Input))
		}
	}
	return out
}

// toolResultLines renders a tool result compactly (the first line + a truncation marker), so the tail
// shows what came back without dumping a whole file.
func toolResultLines(rawMsg json.RawMessage) []string {
	var m workerStreamMessage
	if len(rawMsg) == 0 || json.Unmarshal(rawMsg, &m) != nil {
		return nil
	}
	var out []string
	for _, b := range m.Content {
		if b.Type != "tool_result" {
			continue
		}
		// tool_result content can be a string or a nested block array; the flattened Text covers the
		// common string case, and we only surface a short preview regardless.
		txt := strings.TrimSpace(b.Text)
		if txt == "" {
			continue
		}
		first := strings.SplitN(txt, "\n", 2)
		preview := first[0]
		if len(first) > 1 || len(preview) > 120 {
			preview += " …"
		}
		out = append(out, "  ⤷ "+preview)
	}
	return out
}

// workerSkillNames is the injected skill-name set for THIS crank process, set once (alongside
// workerSkillManifest) from the run's org skills. The streaming worker watches the event stream for
// one of these being invoked, to power the library's "invoked in M runs" telemetry. Empty for
// `ptln work` / describe (no org skills), so those runs do no detection and pay nothing.
var workerSkillNames []string

// skillWatch derives INVOCATION telemetry from a streamed run: which injected skills the agent
// actually used. It is claude-only by nature — only the stream-json path exposes structured tool
// events; the buffered (non-claude) path has nothing to read, so those skills stay uninvoked (an
// honest UNDER-count, never an over-count). Matching is scoped to the injected set, so a stray path
// mentioning a non-injected name can never produce a false positive, and the injected manifest (which
// rides in the PROMPT, not a tool_use block) is never mistaken for a use.
type skillWatch struct {
	names []string        // injected skill names to watch (nil = watch nothing)
	seen  map[string]bool // names detected as invoked so far
}

func newSkillWatch(names []string) *skillWatch {
	return &skillWatch{names: names, seen: map[string]bool{}}
}

// inspect reads one stream-json line. On an assistant message it checks every tool_use block for a
// skill activation: (a) claude's `Skill` tool naming an injected skill, or (b) any tool arg whose
// path touches an injected skill's materialized dir (…/skills/<name>/… — a read of SKILL.md, or a
// run of a bundled scripts/… file). Only assistant tool_use blocks are inspected, never the prompt,
// so the injected manifest can't self-trigger.
func (w *skillWatch) inspect(raw []byte) {
	if w == nil || len(w.names) == 0 || len(w.seen) == len(w.names) {
		return // nothing to watch, or already found them all
	}
	var ev workerStreamEvent
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "assistant" {
		return
	}
	var m workerStreamMessage
	if len(ev.Message) == 0 || json.Unmarshal(ev.Message, &m) != nil {
		return
	}
	for _, b := range m.Content {
		if b.Type != "tool_use" {
			continue
		}
		argsLower := strings.ToLower(string(b.Input))
		isSkillTool := strings.EqualFold(b.Name, "Skill")
		for _, name := range w.names {
			if w.seen[name] {
				continue
			}
			// (a) any tool arg references the skill's materialized dir …/skills/<name>/…
			if strings.Contains(argsLower, "/skills/"+strings.ToLower(name)+"/") {
				w.seen[name] = true
				continue
			}
			// (b) claude's Skill tool names the skill directly in its input.
			if isSkillTool && skillNameInInput(b.Input, name) {
				w.seen[name] = true
			}
		}
	}
}

// invoked returns the detected skills, sorted for a stable report. nil when none were seen.
func (w *skillWatch) invoked() []string {
	if w == nil || len(w.seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(w.seen))
	for n := range w.seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// skillNameInInput reports whether any string value in a tool's flat input equals name (case-
// insensitive) — the shape of claude's Skill tool activation ({"command":"<name>"} / {"name":"<name>"}),
// without pinning the exact key claude uses.
func skillNameInInput(input json.RawMessage, name string) bool {
	if len(input) == 0 {
		return false
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return false
	}
	for _, v := range m {
		if s, ok := v.(string); ok && strings.EqualFold(strings.TrimSpace(s), name) {
			return true
		}
	}
	return false
}

// toolArgHint pulls a short, safe hint out of a tool's input (a file path, a command's first token, a
// pattern) so "→ Edit(src/foo.go)" is more legible than a bare "→ Edit". Never dumps the whole input.
func toolArgHint(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "pattern", "command", "notebook_path", "url", "query"} {
		if v, ok := m[k]; ok {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s == "" {
				continue
			}
			// A command can be long/multiline — keep the first token only.
			if k == "command" {
				s = strings.SplitN(s, "\n", 2)[0]
				if len(s) > 80 {
					s = s[:80] + "…"
				}
			}
			return "(" + s + ")"
		}
	}
	return ""
}
