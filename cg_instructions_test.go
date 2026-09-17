package main

import (
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The briefing is the only thing the model is guaranteed to read before it acts, so its failure mode
// is a model that confidently misdiagnoses. Each state must name the state AND the single next move.

func TestBriefingNamesTheNextActionInEveryState(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		thread  string
		dir     string
		mustSay []string
		mustNot []string
	}{
		{
			name:    "signed out says so and stops",
			token:   "",
			thread:  "fa365970-def0-4321-a8f1-630a723ef35c",
			mustSay: []string{"NOT SIGNED IN", "ptln login"},
			// Listing planning steps to a session that cannot authenticate invites twenty wasted turns.
			mustNot: []string{"Context thread fa365970 is bound"},
		},
		{
			name:    "no thread points at the ONE command that fixes it",
			token:   "tok",
			thread:  "",
			mustSay: []string{"NO CONTEXT THREAD", "create_project"},
		},
		{
			name:    "bound thread reports itself",
			token:   "tok",
			thread:  "fa365970-def0-4321-a8f1-630a723ef35c",
			mustSay: []string{"fa365970", "bound"},
			mustNot: []string{"NO CONTEXT THREAD", "NOT SIGNED IN"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if tt.token != "" {
				if err := api.SaveToken(tt.token); err != nil {
					t.Fatal(err)
				}
			}
			got := cgInstructions(tt.thread)
			for _, m := range tt.mustSay {
				if !strings.Contains(got, m) {
					t.Errorf("briefing never says %q:\n%s", m, got)
				}
			}
			for _, m := range tt.mustNot {
				if strings.Contains(got, m) {
					t.Errorf("briefing wrongly says %q in this state", m)
				}
			}
		})
	}
}

// The lesson that cost a day: a not-found from these tools means the REPO is not set up. A model that
// blames the user's account sends them hunting through accounts and orgs for a problem that is not
// there. The briefing must say this explicitly, in every state.
func TestBriefingForbidsBlamingTheAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := api.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"", "fa365970-def0-4321-a8f1-630a723ef35c"} {
		got := cgInstructions(thread)
		if !strings.Contains(got, "ptln doctor") {
			t.Error("the briefing never tells the model to ask the machine instead of guessing")
		}
		if !strings.Contains(strings.ToLower(got), "not that the user's account is wrong") {
			t.Error("the briefing does not warn against blaming the account for a not-found")
		}
	}
}

// Latency guard. initialize BLOCKS the start of the session — the briefing must be assembled from
// local state only. A network call here would hang the user's editor on a slow control plane.
func TestBriefingIsCheapEnoughToBlockSessionStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := api.SaveToken("tok"); err != nil {
		t.Fatal(err)
	}
	// Point the API at a black hole: if the briefing reached the network it would stall here, and
	// with no server listening it would at minimum have to handle an error it cannot report.
	t.Setenv("PARTYLINE_API", "http://127.0.0.1:1")
	done := make(chan string, 1)
	go func() { done <- cgInstructions("") }()
	select {
	case out := <-done:
		if out == "" {
			t.Error("briefing came back empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cgInstructions took over 2s — it is doing I/O it should not; session start would hang")
	}
}
