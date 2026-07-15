package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
		rlReset      time.Time // latest quota-reset time seen from a blocking rate_limit_event (zero = none)
	)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20) // agent messages + tool results can be large
	for sc.Scan() {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		// Capture the resume handle from the FIRST event that carries a session id (Claude stamps it
		// on the init `system` frame, before any result). Doing it early — not just from the terminal
		// `result` event — means an INTERRUPTED run (timeout, rate limit, kill) still has a handle to
		// resume from, which is exactly the case resume exists for.
		if resumeHandle == "" {
			if id := parseSessionID(raw); id != "" {
				resumeHandle = id
			}
		}
		if reset, blocked := parseRateLimit(raw); blocked {
			rlReset = reset
			onLog("⏸ rate limit reached — resets " + reset.Format("Jan 2 3:04 PM"))
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

	if ctx.Err() == context.DeadlineExceeded {
		onLog("■ hit the time budget — stopped")
		fallback := resultText
		if fallback == "" {
			fallback = stderr.String()
		}
		// Keep the resume handle on a timeout — the whole point is to continue from where it stopped.
		return workerOutcome{text: fallback, tokens: resultTokens, resumeHandle: resumeHandle, rateReset: rlReset}, fmt.Errorf("hit the %s time budget — stopped", timeout)
	}
	if sawResult {
		return workerOutcome{text: resultText, tokens: resultTokens, resumeHandle: resumeHandle, rateReset: rlReset}, waitErr
	}
	// No structured result event (unexpected output / a non-claude engine later): degrade like the
	// buffered path — unknown (0) usage, raw stderr as the text — but keep any handle/reset we saw.
	return workerOutcome{text: stderr.String(), resumeHandle: resumeHandle, rateReset: rlReset}, waitErr
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
func parseRateLimit(raw []byte) (resetAt time.Time, blocked bool) {
	var e struct {
		Type string `json:"type"`
		Info struct {
			Status        string `json:"status"`
			OverageStatus string `json:"overageStatus"`
			ResetsAt      int64  `json:"resetsAt"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Type != "rate_limit_event" {
		return time.Time{}, false
	}
	// Only an ACTUAL block pauses the run — status rejected/exhausted/blocked. A "status:allowed"
	// event (even with overageStatus:rejected, i.e. "no overage left" — exactly the event we saw)
	// means the request went through, so it must NOT pause a healthy run. Under-triggering is safe
	// (the run just falls back to normal failure handling); over-triggering would halt good runs.
	blockedStatus := e.Info.Status == "rejected" || e.Info.Status == "exhausted" || e.Info.Status == "blocked"
	if blockedStatus && e.Info.ResetsAt > 0 {
		return time.Unix(e.Info.ResetsAt, 0), true
	}
	return time.Time{}, false
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
