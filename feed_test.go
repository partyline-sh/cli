package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/bus"
)

// The feed shows ACTIVITY. Rosters and our own heartbeat are bookkeeping the protocol needs and
// a person does not — a feed that narrated every presence tick would scroll its real content
// away within a minute.
func TestTheFeedIgnoresProtocolBookkeeping(t *testing.T) {
	roster, _ := json.Marshal(map[string]any{"peers": []string{"u1:mbp"}, "you": "u2:laptop"})
	if got := feedText(bus.Envelope{Type: bus.TypePresence, Payload: roster}); got != "" {
		t.Errorf("a roster produced a line: %q", got)
	}
	if got := feedText(bus.Envelope{Type: bus.TypePresence, Project: "acr", Payload: json.RawMessage(`{}`)}); got == "" {
		t.Error("a teammate arriving should produce a line")
	}
	left, _ := json.Marshal(map[string]string{"left": "u1:mbp"})
	if got := feedText(bus.Envelope{Type: bus.TypePresence, Project: "acr", Payload: left}); !strings.Contains(got, "left") {
		t.Errorf("a departure should say so, got %q", got)
	}
}

func TestTheFeedShowsWhatWasAskedAndRecorded(t *testing.T) {
	ask, _ := json.Marshal(map[string]string{"text": "does fleet retry the POS callback?"})
	if got := feedText(bus.Envelope{Type: bus.TypeAsk, Payload: ask}); !strings.Contains(got, "POS callback") {
		t.Errorf("an ask should show its text, got %q", got)
	}
	if got := feedText(bus.Envelope{Type: bus.TypeMemoryChanged, Project: "acr"}); !strings.Contains(got, "acr") {
		t.Errorf("a recorded fact should name its project, got %q", got)
	}
}

// An identity is built for routing, not reading. A 42-column pane has no room for a user id and
// a per-process suffix, and "darcy-mbp-9f3a1c2b" tells a person nothing the first half does not.
func TestIdentitiesAreShortenedForReading(t *testing.T) {
	for id, want := range map[string]string{
		"u_01H8ABCDEF:darcy-mbp-9f3a1c2b": "darcy-mbp",
		"u_01H8ABCDEF:monolith-1a2b3c4d":  "monolith",
		"bus":                             "partyline",
		"":                                "partyline",
	} {
		if got := shortWho(id); got != want {
			t.Errorf("shortWho(%q) = %q, want %q", id, got, want)
		}
	}
}

// The feed is a window on now, not a log. Without a bound, a machine left open for a week holds
// every event it ever saw.
func TestTheFeedKeepsABoundedWindow(t *testing.T) {
	s := &feedState{}
	for i := 0; i < feedMax+50; i++ {
		s.add(feedLine{At: time.Now(), Who: "x", Text: "line"})
	}
	if got := len(s.snapshot()); got != feedMax {
		t.Fatalf("kept %d lines, want the cap of %d", got, feedMax)
	}
}
