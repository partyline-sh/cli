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
var memoryHookEngine = engineAncestor

func memoryHookMain() {
	ensureWatcher()
	var context strings.Builder
	defer func() { _, engine := memoryHookEngine(); fmt.Print(memoryHookOutput(engine, context.String())) }()
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	// The loop's state line and messages (loop_inbox.go) ride on this hook because running
	// sessions already have it. Before the memory switch: turning memory refresh off must not cut
	// a loop off from its session.
	context.WriteString(loopGuidePointerForSession(in.SessionID))
	context.WriteString(loopPromptContext(chatSelfKeys(in.SessionID)))
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PARTYLINE_MEMORY_REFRESH")), "off") {
		return
	}
	dir := in.Cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}
	// REPO-LOCAL FIRST, matching workspaceHere(). A checkout with facts in it briefs the session
	// with no project, no label and no remote — which is the only way a teammate who just cloned
	// gets context on their first turn.
	wsDir, label, _, ok := memoryTarget(dir)
	if !ok {
		// A git repo with no memory yet: offer to set it up, once per repo (memory_offer.go).
		context.WriteString(memoryOffer(dir))
		return
	}
	if !isRepoLocalMemory(wsDir) {
		// Take teammates' latest before deciding there is nothing new. Only for a partyline-owned
		// clone: pulling someone's own repo under them mid-session is not ours to do.
		_, _ = memoryPull(wsDir)
	}
	ws := &projectWorkspace{Dir: wsDir, Label: label}

	facts, err := readFacts(ws.Dir, false)
	if err != nil || len(facts) == 0 {
		return
	}
	seen := loadSeen(in.SessionID)
	if seen.LastAt.IsZero() {
		// The first turn is also when to say the memory has fallen behind the code, at most once
		// per memoryDriftThreshold commits (memory_drift.go).
		if isRepoLocalMemory(ws.Dir) {
			context.WriteString(memoryDriftNotice(ws.Dir))
		}
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
	context.WriteString(renderRefresh(ws.Label, ws.repoNameFor(dir), fresh))
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

// memoryHookOutput preserves Claude stdout and uses the structured field for other engines.
func memoryHookOutput(engine, context string) string {
	if context == "" {
		return ""
	}
	if engine != "codex" && engine != "gemini" {
		return context
	}
	// Each engine's schema requires the event the output answers; codex rejects output without it
	// (UserPromptSubmitHookSpecificOutputWire: hookEventName required, no other properties).
	event := "UserPromptSubmit"
	if engine == "gemini" {
		event = "BeforeAgent"
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": context}})
	return string(b) + "\n"
}
