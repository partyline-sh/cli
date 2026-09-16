package main

import (
	"strings"
	"testing"
	"time"
)

// The two things that make ask_session right or dangerously wrong: WHICH session a name resolves to,
// and WHAT comes back as the answer. Both fail silently if they fail — a question delivered to the
// wrong project returns a confident answer about the wrong codebase, and a bad capture returns our
// own prompt as if the model had said it.

var live = []sessionCandidate{
	{Key: "k-fleet", Label: "ACR FLEET MANAGER"},
	{Key: "k-odoo", Label: "ACR ODOO MCP"},
	{Key: "k-ptln", Label: "partyline"},
}

func TestResolveSessionName(t *testing.T) {
	cases := []struct {
		name, ask, wantKey string
		wantErr            string // substring; "" = expect success
	}{
		{"exact", "ACR ODOO MCP", "k-odoo", ""},
		{"case and space insensitive", "  acr odoo mcp  ", "k-odoo", ""},
		{"unique prefix", "ACR FLEET", "k-fleet", ""},
		{"unique substring", "ODOO", "k-odoo", ""},
		{"single word exact", "partyline", "k-ptln", ""},
		// The one that matters. "ACR" prefixes two sessions; guessing would deliver a question about
		// one codebase to an agent steeped in another, and the answer would read as authoritative.
		{"ambiguous refuses", "ACR", "", "matches"},
		{"unknown names what is open", "odoo mcp v2", "", "no session named"},
		{"empty asks for a name", "", "", "name a session"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, err := resolveSessionName(c.ask, live, "")
			if c.wantErr != "" {
				if err == "" {
					t.Fatalf("expected an error containing %q, got key %q", c.wantErr, key)
				}
				if !strings.Contains(err, c.wantErr) {
					t.Fatalf("error %q does not mention %q", err, c.wantErr)
				}
				return
			}
			if err != "" {
				t.Fatalf("unexpected error: %s", err)
			}
			if key != c.wantKey {
				t.Fatalf("got %q, want %q", key, c.wantKey)
			}
		})
	}
}

// An exact name must keep winning after someone opens a longer session that contains it — otherwise
// addressing silently breaks the day a "… STAGING" tab appears.
func TestExactBeatsLongerMatch(t *testing.T) {
	l := append(append([]sessionCandidate{}, live...), sessionCandidate{Key: "k-odoo2", Label: "ACR ODOO MCP STAGING"})
	key, err := resolveSessionName("ACR ODOO MCP", l, "")
	if err != "" || key != "k-odoo" {
		t.Fatalf("exact match lost to a longer one: key=%q err=%q", key, err)
	}
}

// Asking yourself is a deadlock dressed as a feature: the asking session can't answer while it waits.
func TestSelfIsNotAddressable(t *testing.T) {
	_, err := resolveSessionName("ACR ODOO MCP", live, "k-odoo")
	if err == "" {
		t.Fatal("resolved to the asking session itself")
	}
}

func TestExtractAnswer(t *testing.T) {
	t.Run("takes the reply, not our own prompt", func(t *testing.T) {
		screen := askPrompt("ACR FLEET MANAGER", "which port does the odoo mcp bind?") +
			"\n\nIt binds 8069 in docker-compose, overridden by ODOO_PORT.\n" + askDoneMarker + "\n"
		got, ok := extractAnswer(screen)
		if !ok {
			t.Fatal("no answer extracted")
		}
		if strings.Contains(got, "which port does") {
			t.Fatalf("the question came back as the answer:\n%s", got)
		}
		if !strings.Contains(got, "8069") {
			t.Fatalf("lost the actual answer:\n%s", got)
		}
	})

	// THE REGRESSION. Our own prompt necessarily contains the marker (that is how the target learns
	// what to end with), so a naive "last marker wins" scraper hands back the echoed question as a
	// confident answer. The first version of extractAnswer did exactly that; this is what caught it.
	t.Run("an echoed prompt with no reply yet is NOT an answer", func(t *testing.T) {
		if got, ok := extractAnswer(askPrompt("ACR FLEET MANAGER", "which port?")); ok {
			t.Fatalf("returned our own prompt as an answer:\n%q", got)
		}
	})

	t.Run("no marker means not finished, not empty", func(t *testing.T) {
		// Mid-turn output must read as "still working", never as a complete short answer.
		if _, ok := extractAnswer("still thinking about the port…"); ok {
			t.Fatal("claimed an answer with no end marker")
		}
	})

	t.Run("marker with nothing before it is not an answer", func(t *testing.T) {
		if _, ok := extractAnswer(askDoneMarker); ok {
			t.Fatal("empty body reported as an answer")
		}
	})

	t.Run("takes the LAST marker when a session answered twice", func(t *testing.T) {
		screen := "first reply\n" + askDoneMarker + "\nlater: actually it is 8072\n" + askDoneMarker
		got, ok := extractAnswer(screen)
		if !ok || !strings.Contains(got, "8072") {
			t.Fatalf("did not take the most recent answer: ok=%v got=%q", ok, got)
		}
	})
}

// The framing is the ONLY brake on a target that holds write and shell. If these instructions drift
// out, the feature quietly becomes "one agent can task another".
func TestPromptTellsTheTargetNotToAct(t *testing.T) {
	p := askPrompt("ACR FLEET MANAGER", "why is the build failing?")
	for _, want := range []string{"ACR FLEET MANAGER", "Don't start an investigation", "don't edit files", "don't run commands", askDoneMarker} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lost %q:\n%s", want, p)
		}
	}
}

func TestAskExpiry(t *testing.T) {
	now := time.Now()
	open := sessionAsk{Status: askOpen, AskedAt: now.Add(-askTTL - time.Minute)}
	if !open.expired(now) {
		t.Fatal("an old open ask should expire")
	}
	// A finished ask is never "expired" — it has an answer someone may still collect.
	done := sessionAsk{Status: askAnswered, AskedAt: now.Add(-24 * time.Hour)}
	if done.expired(now) {
		t.Fatal("an answered ask expired and would lose its answer")
	}
}
