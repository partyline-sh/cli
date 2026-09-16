package main

import (
	"os"
	"testing"
	"time"
	"unicode/utf8"
)

// The tray's headline was "Daemon: ● connected" driven by serviceActive() — `launchctl list`
// exiting 0. That is a registered job, not a reachable server, and it read "connected" for the
// whole of a fleet-wide outage while every daemon beat at a hostname that had stopped listening.
// These pin the replacement: connected means a heartbeat the instance ACCEPTED, recently.

func linkHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PARTYLINE_API", "https://ptln.example.com")
}

func TestAnAcceptedBeatIsConnected(t *testing.T) {
	linkHome(t)
	now := time.Now()
	recordBeat("https://ptln.example.com", nil, now)

	h := describeLink(true, now.Add(30*time.Second))
	if !h.Connected {
		t.Fatalf("a beat 30s ago must read as connected: %+v", h)
	}
	if h.Endpoint != "ptln.example.com" {
		t.Errorf("the line must name what it is connected TO; got %q", h.Endpoint)
	}
}

// The case the old indicator got wrong. The process is up; the instance has gone quiet.
func TestARunningDaemonWithNoRecentBeatIsNotConnected(t *testing.T) {
	linkHome(t)
	now := time.Now()
	recordBeat("https://ptln.example.com", nil, now)

	h := describeLink(true, now.Add(6*time.Minute))
	if h.Connected {
		t.Fatal("six minutes of silence must not read as connected")
	}
	if !containsStr(h.Detail, "no reply") || !containsStr(h.Detail, "6m") {
		t.Errorf("it must say how long it has been quiet; got %q", h.Detail)
	}
}

// One missed beat is a blip on any real network; flapping the light for it trains you to ignore it.
func TestOneMissedBeatDoesNotFlapTheLight(t *testing.T) {
	linkHome(t)
	now := time.Now()
	recordBeat("https://ptln.example.com", nil, now)

	if h := describeLink(true, now.Add(75*time.Second)); !h.Connected {
		t.Fatalf("a single missed beat must stay connected; got %+v", h)
	}
}

// A failure keeps the last SUCCESS time, so the menu can say "no reply for 4m" rather than losing
// track of when it last worked.
func TestAFailureRemembersWhenItLastWorked(t *testing.T) {
	linkHome(t)
	now := time.Now()
	recordBeat("https://ptln.example.com", nil, now)
	recordBeat("https://ptln.example.com", os.ErrDeadlineExceeded, now.Add(time.Minute))

	st, ok := readLinkState()
	if !ok {
		t.Fatal("state should exist")
	}
	if st.OKAt == "" {
		t.Fatal("a failed beat must not erase the last successful one")
	}
	if st.Err == "" {
		t.Fatal("the failure reason must be recorded")
	}
}

// "Stopped" and "running but unreachable" have completely different fixes; the old single word
// collapsed them.
func TestStoppedIsDistinctFromUnreachable(t *testing.T) {
	linkHome(t)
	now := time.Now()
	recordBeat("https://ptln.example.com", nil, now.Add(-time.Hour))

	stopped := describeLink(false, now)
	unreachable := describeLink(true, now)
	if stopped.Connected || unreachable.Connected {
		t.Fatal("neither is connected")
	}
	if stopped.Detail == unreachable.Detail {
		t.Fatalf("a stopped daemon and an unreachable instance must not read the same: %q", stopped.Detail)
	}
}

// A daemon that has never beaten (just started, or an older build) must not claim either state.
func TestNoBeatYetSaysSo(t *testing.T) {
	linkHome(t)
	h := describeLink(true, time.Now())
	if h.Connected {
		t.Fatal("no beat has happened; it cannot be connected")
	}
	if !containsStr(h.Detail, "starting") {
		t.Errorf("got %q", h.Detail)
	}
}

// The error text reaches a menu bar, so it is bounded.
func TestErrorTextIsBounded(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	if got := trimLinkErr(long); len([]rune(got)) > linkErrMax {
		t.Fatalf("error text must be bounded for a menu; got %d runes", len([]rune(got)))
	}
	// Multi-byte input must not be sliced mid-sequence into invalid UTF-8, which a menu renders as
	// a replacement glyph — turning a diagnostic into mojibake.
	wide := ""
	for i := 0; i < 300; i++ {
		wide += "é"
	}
	if got := trimLinkErr(wide); !utf8.ValidString(got) {
		t.Fatal("truncation produced invalid UTF-8")
	}
	if got := trimLinkErr("a\nb"); containsStr(got, "\n") {
		t.Fatal("newlines must not reach a single-line menu item")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}
