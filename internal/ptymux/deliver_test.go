package ptymux

import (
	"strings"
	"testing"
)

// PARTYLINE_SESSION_KEY is what lets an ask made by an AGENT (the cg-mcp child of the engine) be
// attributed to the session it came from — without it, an answer arriving later has no address.
func TestChildEnvCarriesSessionKey(t *testing.T) {
	got := strings.Join(childEnv(Spec{Key: "s-42", Thread: "t-1", Engine: "claude"}), "\n")
	if !strings.Contains(got, "PARTYLINE_SESSION_KEY=s-42") {
		t.Fatalf("session key not exported to the child:\n%s", got)
	}
}

// A nested launch must never inherit a sibling's identity: an inherited key would make an answer land
// in the wrong agent's prompt, which is the one thing delivery must never do.
func TestChildEnvNeverInheritsAnotherSessionsKey(t *testing.T) {
	t.Setenv("PARTYLINE_SESSION_KEY", "s-stale")
	env := childEnv(Spec{Key: "s-new"})
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PARTYLINE_SESSION_KEY=") {
			n++
			if kv != "PARTYLINE_SESSION_KEY=s-new" {
				t.Fatalf("inherited a stale key: %s", kv)
			}
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one session key in the child env, got %d", n)
	}
	// And a keyless spec exports none at all rather than leaking the ambient one.
	for _, kv := range childEnv(Spec{}) {
		if strings.HasPrefix(kv, "PARTYLINE_SESSION_KEY=") {
			t.Fatalf("a keyless session exported %s", kv)
		}
	}
}

// unsubmitted is the "the human has something typed" signal that gates auto-submit. It must count
// forwarded bytes and RESET on a submit, so a prompt the human already sent doesn't read as dirty.
func TestUnsubmittedInputTracking(t *testing.T) {
	cases := []struct {
		name string
		feed []string
		want int
	}{
		{"nothing typed", nil, 0},
		{"partial prompt", []string{"fix the ", "bug"}, 11},
		{"submitted, so clean", []string{"fix the bug", "\r"}, 0},
		{"typed again after submitting", []string{"fix it\r", "ne"}, 2},
		{"LF counts as a submit too", []string{"hello\n"}, 0},
		{"an arrow key is unsubmitted input", []string{"\x1b[A"}, 3},
	}
	for _, c := range cases {
		ch := &child{}
		for _, s := range c.feed {
			ch.noteInput([]byte(s))
		}
		if ch.unsubmitted != c.want {
			t.Errorf("%s: unsubmitted = %d, want %d", c.name, ch.unsubmitted, c.want)
		}
	}
}

// SessionByKey must never resolve an empty key — "no origin recorded" has to mean "no target", not
// "the first session I find".
func TestSessionByKeyRefusesEmpty(t *testing.T) {
	mx := &Mux{children: []*child{{key: "s-1"}}}
	if _, _, _, ok := mx.SessionByKey(""); ok {
		t.Fatal("an empty key must not resolve to any session")
	}
	if _, _, _, ok := mx.SessionByKey("s-2"); ok {
		t.Fatal("an unknown key must not resolve")
	}
}
