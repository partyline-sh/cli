package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// keep-going (E4.0) — the SAFE anti-Ralph auto-continue, and the mechanism the E4 conductor
// reuses. A Claude `Stop` hook that forces the agent to keep working after each turn until
// EITHER a hard turn cap is exhausted OR the agent emits the done sentinel. It is NOT a timer
// and NOT PTY injection: the cap is enforced by us on every Stop, so a runaway loop is
// impossible by construction. Fail-safe throughout — any error lets the session stop normally.
//
//	ptln new claude --keep-going 20 --goal "work through the open issues one at a time"
//	ptln keepgoing status | off
const keepGoingDone = "<<keep-going-done>>"

type keepGoingState struct {
	Remaining int    `json:"remaining"` // continuations left before we stop forcing
	Goal      string `json:"goal"`      // standing instruction fed on each continuation
	StartedAt string `json:"started_at"`
}

func keepGoingDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "keepgoing")
}
func keepGoingPath(key string) string { return filepath.Join(keepGoingDir(), key+".json") }

func loadKeepGoing(key string) (keepGoingState, bool) {
	b, err := os.ReadFile(keepGoingPath(key))
	if err != nil {
		return keepGoingState{}, false
	}
	var s keepGoingState
	if json.Unmarshal(b, &s) != nil {
		return keepGoingState{}, false
	}
	return s, true
}

func saveKeepGoing(key string, s keepGoingState) error {
	if err := os.MkdirAll(keepGoingDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(keepGoingPath(key), append(b, '\n'), 0o600)
}

// keepGoingDecide is the pure heart (unit-tested): given the state and the agent's last message
// text, return whether to force another turn, the instruction to inject, and the next state.
// Stops when the cap is exhausted OR the agent signalled done — the two hard brakes.
func keepGoingDecide(s keepGoingState, lastText string) (cont bool, reason string, next keepGoingState) {
	if s.Remaining <= 0 {
		return false, "", s
	}
	if strings.Contains(lastText, keepGoingDone) {
		return false, "", keepGoingState{} // agent declared done → stop, clear
	}
	next = s
	next.Remaining = s.Remaining - 1
	goal := strings.TrimSpace(s.Goal)
	if goal == "" {
		goal = "Continue the task."
	}
	plural := "s"
	if next.Remaining == 1 {
		plural = ""
	}
	reason = fmt.Sprintf(
		"%s\n\nKeep working autonomously (%d continuation%s left before I stop nudging). "+
			"When — and only when — the whole task is genuinely complete, print the exact token %s on its own line and stop.",
		goal, next.Remaining, plural, keepGoingDone)
	return true, reason, next
}

// keepGoingSettings is the claude `--settings` JSON that installs the Stop hook for one session.
func keepGoingSettings(key string) string {
	return sessionSettings(map[string]any{"Stop": []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": fmt.Sprintf("%s keepgoing-hook --key %s", selfExe(), key)}},
	}}})
}

// sessionSettings renders the --settings blob for a launched session, ALWAYS including the
// memory refresh hook.
//
// The refresh has to be here rather than at session start, because sessions do not end: this
// machine has had agent sessions alive for a fortnight, so a session briefed on Monday would
// never learn what a teammate recorded on Thursday. UserPromptSubmit fires every turn, prints
// nothing on the overwhelming majority of them, and costs one local read when it does.
//
// Merging matters: keep-going and the refresh are different hook events, and an earlier version
// of this that just handed Claude a fresh settings object would have silently disarmed whichever
// feature was configured second.
func sessionSettings(extra map[string]any) string {
	hooks := map[string]any{
		"UserPromptSubmit": []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": selfExe() + " memory-hook"}},
		}},
		// Capture what the session learned without anyone remembering to. It never blocks the
		// stop — see memory_capture.go. Merged with, not replacing, any Stop hook in `extra`.
		"Stop": []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": selfExe() + " memory-capture-hook"}},
		}},
	}
	for event, spec := range extra {
		// `extra` carries the keep-going Stop hook when it is armed. Overwriting would silently
		// disable capture for exactly the long autonomous sessions that learn the most, so the
		// two are appended rather than one replacing the other.
		if cur, ok := hooks[event].([]any); ok {
			if add, ok := spec.([]any); ok {
				hooks[event] = append(cur, add...)
				continue
			}
		}
		hooks[event] = spec
	}
	b, _ := json.Marshal(map[string]any{"hooks": hooks})
	return string(b)
}

// armKeepGoing writes fresh state and returns its key (used by --keep-going at launch).
func armKeepGoing(count int, goal string) (string, error) {
	key := fmt.Sprintf("kg-%d", time.Now().UnixNano())
	return key, saveKeepGoing(key, keepGoingState{Remaining: count, Goal: strings.TrimSpace(goal), StartedAt: time.Now().Format(time.RFC3339)})
}

// keepgoingHookMain is the Stop hook (ptln keepgoing-hook --key <k>). Claude runs it when the
// agent finishes a turn: read state + the transcript tail, decide, persist, and to force
// another turn print {"decision":"block","reason":...}. Any error → allow the stop (fail-safe).
func keepgoingHookMain(args []string) {
	key := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--key" && i+1 < len(args) {
			key = args[i+1]
			i++
		}
	}
	var in struct {
		TranscriptPath string `json:"transcript_path"`
	}
	if b, _ := io.ReadAll(os.Stdin); len(b) > 0 {
		_ = json.Unmarshal(b, &in)
	}
	s, ok := loadKeepGoing(key)
	if !ok {
		return // no/again-cleared state → let it stop
	}
	cont, reason, next := keepGoingDecide(s, transcriptTail(in.TranscriptPath))
	if !cont {
		_ = os.Remove(keepGoingPath(key)) // finished (cap hit or done) → clean up
		return
	}
	if saveKeepGoing(key, next) != nil {
		return // couldn't persist → don't risk an unbounded loop; allow stop
	}
	out, _ := json.Marshal(map[string]any{"decision": "block", "reason": reason})
	fmt.Println(string(out))
}

// transcriptTail returns the last few KB of the transcript file as raw text — enough to
// substring-match the done sentinel without depending on the transcript's exact JSON shape.
func transcriptTail(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const window = 8192
	if fi, err := f.Stat(); err == nil && fi.Size() > window {
		_, _ = f.Seek(-window, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	return string(b)
}

// keepgoingMain is the user-facing command: `ptln keepgoing [status|off]`.
func keepgoingMain(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		entries, _ := os.ReadDir(keepGoingDir())
		n := 0
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if s, ok := loadKeepGoing(strings.TrimSuffix(e.Name(), ".json")); ok {
				n++
				fmt.Printf("  %d continuations left · %s\n", s.Remaining, s.Goal)
			}
		}
		if n == 0 {
			fmt.Println("no keep-going sessions armed")
		}
	case "off":
		_ = os.RemoveAll(keepGoingDir())
		fmt.Println("✓ keep-going disarmed (all sessions)")
	default:
		fatal(fmt.Errorf("usage: ptln keepgoing [status|off]"))
	}
}
