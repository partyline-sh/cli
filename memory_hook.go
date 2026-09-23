package main

// KEEPING A LONG-LIVED SESSION CURRENT.
//
// A brief at session start is worthless to the way people actually work: sessions run for days —
// this machine has had claude sessions alive for two weeks — so a session started on Monday would
// never learn what a teammate recorded on Thursday, and "capture at session end" would never fire
// at all. Sessions are unbounded; TURNS are not. So the unit is the turn.
//
// This is the read half: a UserPromptSubmit hook that prints what is NEW since this session last
// saw anything. Claude Code adds a UserPromptSubmit hook's stdout to the turn's context, so the
// agent is re-briefed continuously without the human doing anything and without the agent having
// to decide to ask.
//
// It prints NOTHING when nothing changed, which is almost every turn. A hook that speaks on every
// prompt would be a tax on every turn and would train people to ignore it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// memoryRefreshCap bounds one injection. A teammate who recorded twenty facts overnight must not
// dump twenty into the middle of somebody's work: the newest few, and the rest are one command away.
const memoryRefreshCap = 5

// seenMarker remembers how far a session has been briefed.
type seenMarker struct {
	LastAt  time.Time `json:"last_at"`
	Project string    `json:"project"`
}

func seenMarkerPath(sessionID string) string {
	return filepath.Join(stateDir(), "memory-seen", sessionID+".json")
}

func loadSeen(sessionID string) seenMarker {
	var m seenMarker
	b, err := os.ReadFile(seenMarkerPath(sessionID))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveSeen(sessionID string, m seenMarker) {
	_ = os.MkdirAll(filepath.Dir(seenMarkerPath(sessionID)), 0o755)
	b, _ := json.Marshal(m)
	_ = os.WriteFile(seenMarkerPath(sessionID), b, 0o644)
}

// memoryHookMain is `ptln memory-hook`, wired as a session's UserPromptSubmit hook. Claude passes
// the session id and cwd on stdin. Failure is silent by design: a hook that errors into a person's
// prompt is worse than a hook that misses one refresh.
func memoryHookMain() {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PARTYLINE_MEMORY_REFRESH")), "off") {
		return
	}
	dir := in.Cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}
	ws, ok := projectForDir(dir)
	if !ok {
		return
	}
	// Take teammates' latest before deciding there is nothing new. Cheap against a local clone,
	// and skipped entirely when the project has no remote yet.
	_, _ = memoryPull(ws.Dir)

	facts, err := readFacts(ws.Dir, false)
	if err != nil || len(facts) == 0 {
		return
	}
	seen := loadSeen(in.SessionID)
	if seen.LastAt.IsZero() {
		// First turn of a session that was already running when memory arrived, or a fresh one:
		// mark the current state as seen WITHOUT printing a wall of history. The session-start
		// brief covers what exists; this hook's job is only what changes from here.
		saveSeen(in.SessionID, seenMarker{LastAt: facts[0].At, Project: ws.Label})
		return
	}
	fresh := newerThan(facts, seen.LastAt)
	if len(fresh) == 0 {
		return
	}
	saveSeen(in.SessionID, seenMarker{LastAt: fresh[0].At, Project: ws.Label})
	fmt.Print(renderRefresh(ws.Label, ws.repoNameFor(dir), fresh))
}

// newerThan returns facts recorded after t, newest first (readFacts already sorts).
func newerThan(facts []fact, t time.Time) []fact {
	var out []fact
	for _, f := range facts {
		if f.At.After(t) {
			out = append(out, f)
		}
	}
	return out
}

// renderRefresh is what lands mid-session. Shorter than the opening brief and explicit that it is
// new, because an agent reading it needs to know this supersedes what it was told before.
func renderRefresh(label, repo string, fresh []fact) string {
	shown := fresh
	extra := 0
	if len(shown) > memoryRefreshCap {
		extra, shown = len(shown)-memoryRefreshCap, shown[:memoryRefreshCap]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "☎ %s — new since this session was briefed:\n", label)
	for _, f := range shown {
		scope := "project"
		if f.Repo != "" {
			scope = f.Repo
		}
		fmt.Fprintf(&b, "- [%s · %s] %s (%s, %s)\n", f.Kind, scope, oneLine(f.Body), f.By, f.ID)
	}
	if extra > 0 {
		fmt.Fprintf(&b, "- and %d more: run `ptln memory ls`\n", extra)
	}
	b.WriteString("Treat these as current. If one contradicts what you were told earlier, the newer fact wins and you should say so.\n")
	return b.String()
}
