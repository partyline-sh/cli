package main

// AUTOMATIC CAPTURE — the session records what it learned, without anyone remembering to.
//
// Nobody types "save this to memory". They never will, and a memory that depends on them doing
// so is empty, which is the most likely way this whole system fails. So the human act is
// REJECTING a fact they can see is wrong, not saving one — rejection is rare and motivated,
// saving is frequent and unmotivated.
//
// Two costs have to be separated for that to be safe:
//
//   CAPTURE is generous and automatic. Every session writes what it learned.
//   THE BRIEF is capped at 12 and is a ranked VIEW over what was captured.
//
// Capturing more can therefore never flood the thing people read. That separation is what makes
// "record by default" tolerable; without it, automatic capture would destroy the brief within a
// week and the brief is the whole product.
//
// WHY AT THE END OF A TURN. This runs on the Stop hook, where the session's own context is still
// the freshest account of what just happened. Mining a transcript later, or a summary of a
// summary, is strictly worse information about the same events.
//
// WHAT IT WRITES. Proposals — `by: harvest`, cited, ranked under facts a person recorded. An LLM
// inferred these, and saying so is the difference between a memory people trust and one they
// learn to ignore.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// captureEvery throttles capture. A session Stops many times an hour; distilling on each one
	// would cost tokens continuously and mostly re-read the same work.
	captureEvery = 20 * time.Minute
	// captureMax bounds what one capture may record. A session that "learned" eight things
	// learned approximately none of them, and the cap makes the distiller choose.
	captureMax = 2
	// captureWindow is how much transcript the distiller sees. Enough for recent work, small
	// enough to stay cheap.
	captureWindow = 24 * 1024
	// captureTimeout — this runs detached, but it must not linger forever.
	captureTimeout = 90 * time.Second
)

func captureMarkerPath(session string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '_'
	}, session)
	return filepath.Join(stateDir(), "capture", safe)
}

// captureDue reports whether enough has happened since this session last recorded anything.
func captureDue(session string) bool {
	fi, err := os.Stat(captureMarkerPath(session))
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) > captureEvery
}

func markCaptured(session string) {
	p := captureMarkerPath(session)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	_ = os.WriteFile(p, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
}

// capturePrompt is the whole judgement, so it is written to make NOTHING the easy answer.
//
// A distiller that feels obliged to produce output will produce output, and a memory filled with
// restatements of what someone just did is how the brief stops being read.
func capturePrompt(transcript string) string {
	return `You are reading the tail of a coding session's transcript. Record only what a TEAMMATE
who was not here would need to know in a month, and who does not have this session.

Reply with a JSON array, and prefer the empty array. Most sessions teach nobody anything
durable: they fix a bug, rename a thing, chase a typo. "[]" is the correct answer far more
often than not, and is never a failure.

Record ONLY:
  decision   — a choice made, and why it was made over the alternative
  constraint — something the code must respect from now on
  contract   — an interface or guarantee others depend on
  gotcha     — a trap that cost real time and would cost it again
  question   — something unresolved that needs a human

NEVER record:
  - what was changed, added or refactored (that is what the diff is for)
  - build, test or lint results, or anything true only right now
  - restatements of the task, progress, or what is being worked on next
  - anything you are not certain is still true after this session ends

At most ` + fmt.Sprint(captureMax) + ` items. Each: {"kind":"...","body":"one or two sentences, present tense"}.
Body states the fact itself, not that the session discussed it.

TRANSCRIPT:
` + transcript
}

// captureDistil asks a small model what, if anything, is worth keeping. Errors are silent: a
// failed capture is a fact not written, which is the ordinary case anyway.
func captureDistil(dir, transcript string) []fact {
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", capturePrompt(transcript),
		"--model", "claude-haiku-4-5-20251001", "--output-format", "json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PARTYLINE=1")
	groupSpawn(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	// claude --output-format json wraps the reply in an envelope; fall back to raw output when
	// the shape is not what we expect, so a format change degrades to "no facts" rather than a
	// crash in a detached process nobody is watching.
	var env struct {
		Result string `json:"result"`
	}
	text := string(out)
	if json.Unmarshal(out, &env) == nil && env.Result != "" {
		text = env.Result
	}
	return parseCaptured(text)
}

// parseCaptured pulls the JSON array out of a model's reply, which may be wrapped in prose or a
// fence however firmly it was asked otherwise.
func parseCaptured(text string) []fact {
	start := strings.IndexByte(text, '[')
	end := strings.LastIndexByte(text, ']')
	if start < 0 || end <= start {
		return nil
	}
	var items []struct{ Kind, Body string }
	if json.Unmarshal([]byte(text[start:end+1]), &items) != nil {
		return nil
	}
	var out []fact
	for _, it := range items {
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		body := strings.TrimSpace(it.Body)
		if !validFactKind(kind) || body == "" {
			continue
		}
		out = append(out, fact{Kind: kind, Body: body})
		if len(out) == captureMax {
			break
		}
	}
	return out
}

// memoryCaptureHookMain is `ptln memory-capture-hook` (hidden): the Stop hook that records what
// a session learned.
//
// It NEVER blocks the stop and never prints a decision. The work happens in a detached child, so
// a slow or failed distillation costs the person nothing — the session ends when it ends.
func memoryCaptureHookMain(args []string) {
	// The detached child does the actual work.
	if len(args) > 0 && args[0] == "--run" {
		captureRun()
		return
	}
	var in struct {
		TranscriptPath string `json:"transcript_path"`
		SessionID      string `json:"session_id"`
	}
	b, _ := io.ReadAll(os.Stdin)
	if len(b) > 0 {
		_ = json.Unmarshal(b, &in)
	}
	if in.TranscriptPath == "" {
		return
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PARTYLINE_MEMORY_CAPTURE")), "off") {
		return
	}
	cwd, _ := os.Getwd()
	if _, ok := projectForDir(cwd); !ok {
		return // not in a project — nowhere to record
	}
	if !captureDue(in.SessionID) {
		return
	}
	markCaptured(in.SessionID) // mark BEFORE spawning, so a crashing child cannot spin

	cmd := exec.Command(selfExe(), "memory-capture-hook", "--run",
		"--transcript", in.TranscriptPath, "--session", in.SessionID)
	cmd.Dir = cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	groupSpawn(cmd) // its own process group: the session ending must not take the child with it
	_ = cmd.Start()
	// No output: the stop proceeds.
}

// captureRun is the detached half.
func captureRun() {
	transcript, session := "", ""
	for i, a := range os.Args {
		if a == "--transcript" && i+1 < len(os.Args) {
			transcript = os.Args[i+1]
		}
		if a == "--session" && i+1 < len(os.Args) {
			session = os.Args[i+1]
		}
	}
	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		return
	}
	tail := transcriptWindow(transcript, captureWindow)
	if strings.TrimSpace(tail) == "" {
		return
	}
	facts := captureDistil(cwd, tail)
	if len(facts) == 0 {
		return // the common and correct case
	}
	existing, err := readFacts(ws.Dir, true)
	if err != nil {
		return
	}
	repo := ws.repoNameFor(cwd)
	cite := "session:" + shortSession(session)
	n := 0
	for _, f := range facts {
		if _, dup := dedupeAgainst(existing, f.Body); dup {
			continue
		}
		f.Repo, f.By, f.At, f.Sources = repo, proposedBy, time.Now(), []string{cite}
		if _, err := writeFact(ws.Dir, f); err != nil {
			continue
		}
		existing = append(existing, f)
		n++
	}
	if n == 0 {
		return
	}
	// Share it. A captured fact that never leaves this machine helps nobody, which is the whole
	// point of capturing it.
	_ = memorySync(ws.Dir, fmt.Sprintf("memory: %d observed fact(s)", n))
	pokeStatus()
}

func shortSession(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	if s == "" {
		return "local"
	}
	return s
}

// transcriptWindow is the last n bytes of the transcript.
func transcriptWindow(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	if fi.Size() > n {
		if _, err := f.Seek(-n, 2); err != nil {
			return ""
		}
	}
	b := make([]byte, n)
	c, _ := f.Read(b)
	return string(b[:c])
}
