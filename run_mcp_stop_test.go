package main

import (
	"errors"
	"strings"
	"testing"
)

// stop_run's outcome rule (#794 T1b): the four cases a stopping agent can hit. The one that
// matters most is fail-honest — a failed or unavailable record must still tell the agent to STOP
// and surface the reason in text, never read as "carry on".
func TestRunMCPStopResult(t *testing.T) {
	ok := func(base, token, runID, reason string) error { return nil }
	fail := func(base, token, runID, reason string) error { return errors.New("api down") }

	if text, isErr := runMCPStopResult("r1", "tok", "https://x", "no ticket id in payload", ok); isErr || !strings.Contains(text, "STOPPED") {
		t.Errorf("recorded case wrong: %q err=%v", text, isErr)
	}
	if _, isErr := runMCPStopResult("r1", "tok", "https://x", "   ", ok); !isErr {
		t.Error("empty reason must be an error — the reason IS the product")
	}
	// No run credentials: honest unavailability, still instructs stopping.
	if text, isErr := runMCPStopResult("", "", "https://x", "why", ok); !isErr || !strings.Contains(text, "Stop working now") {
		t.Errorf("unavailable case wrong: %q", text)
	}
	// Recorder fails: NEVER reads as success, still instructs stopping with the reason surfaced.
	if text, isErr := runMCPStopResult("r1", "tok", "https://x", "why", fail); !isErr || !strings.Contains(text, "Stop working anyway") {
		t.Errorf("failed-record case wrong: %q", text)
	}
}

// The prompt must NAME the stop channel when (and only when) the tool is attached — an
// unactionable "stop and explain" is exactly what T1b replaces.
func TestWorkerPromptStopRunBullet(t *testing.T) {
	with := workerPrompt("t", "", false, false, true)
	if !strings.Contains(with, "stop_run") {
		t.Error("hasStopRun prompt must name the stop_run tool")
	}
	without := workerPrompt("t", "", false, false, false)
	if strings.Contains(without, "stop_run") {
		t.Error("a run without the tool must not be told to call it")
	}
}
