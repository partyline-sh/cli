package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tool_activity.go — make it VISIBLE when the agent reaches for partyline.
//
// A session's ☎ tab marker already carries the context state (cyan wired · dim record-only) and
// doubles as the activity light (green working · amber your move). What it could not say is the one
// thing partyline actually knows better than anyone: that THIS turn, the agent called a partyline
// tool. From the outside, recall/remember/read_run are indistinguishable from the model thinking.
//
// cg-mcp runs in a different process from the mux, so — exactly like session-asks and peer-messages —
// a file is the only thing both can see. One file PER SESSION, not a shared map: several cg-mcp
// servers write concurrently (one per live session), and a shared file would need a read-modify-write
// under a cross-process lock to avoid losing stamps. A per-session path makes the write a plain
// atomic replace with no coordination at all.
//
// The payload is deliberately tiny — a tool name and a timestamp. This is a light on a dashboard, not
// a log; run_logs and the thread already hold anything worth keeping.

// toolActivityFresh is how long a call keeps the marker lit. Long enough to see on a fast tool,
// short enough that the light means "now" rather than "at some point".
const toolActivityFresh = 3 * time.Second

func toolActivityDir() string { return filepath.Join(stateDir(), "activity") }

func toolActivityPath(key string) string {
	if key = strings.TrimSpace(key); key != "" && !strings.ContainsAny(key, `/\`) {
		return filepath.Join(toolActivityDir(), key)
	}
	return "" // no key (or a key that could escape the directory) → nothing to write
}

// noteToolActivity stamps that this session just called a partyline tool. Best effort by design: a
// failure here must never turn into a failed tool call, because the tool itself worked.
func noteToolActivity(tool string) {
	path := toolActivityPath(os.Getenv("PARTYLINE_SESSION_KEY"))
	if path == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strings.TrimSpace(tool)), 0o600)
}

// readToolActivity reports the tool this session last called, and whether that was recent enough to
// still count as happening now. The mux resolves this at DRAW time, like StatusFn.
//
// Freshness comes from the file's MODTIME rather than a timestamp inside it: the write is already an
// atomic replace, so the filesystem is keeping the same fact for free, and a clock skew between two
// processes on one machine cannot desync what one of them wrote from what the other reads.
func readToolActivity(key string) (string, bool) {
	path := toolActivityPath(key)
	if path == "" {
		return "", false
	}
	st, err := os.Stat(path)
	if err != nil || time.Since(st.ModTime()) > toolActivityFresh {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// forgetToolActivity clears a session's stamp when the session ends, so a relaunch under the same key
// cannot inherit a light from its predecessor.
func forgetToolActivity(key string) {
	if path := toolActivityPath(key); path != "" {
		_ = os.Remove(path)
	}
}
