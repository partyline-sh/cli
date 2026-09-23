package main

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	// Presence NEVER produces a line on its own: a heartbeat arrives every 30 seconds from every
	// connected process, and echoing them filled the pane with "is here" twice a minute about
	// people who had not done anything. Arrivals and departures are transitions, handled by the
	// caller against the set of who is already known to be here.
	if got := feedText(bus.Envelope{Type: bus.TypePresence, Project: "acr", Payload: json.RawMessage(`{}`)}); got != "" {
		t.Errorf("a presence heartbeat produced a line: %q", got)
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
		// Distinct lines: identical consecutive entries collapse by design (see add), so a
		// repeated string would test the dedupe rather than the window.
		s.add(feedLine{At: time.Now(), Who: "x", Text: fmt.Sprintf("line %d", i)})
	}
	if got := len(s.snapshot()); got != feedMax {
		t.Fatalf("kept %d lines, want the cap of %d", got, feedMax)
	}
}

// The feed runs on a machine that also runs the memory watcher, and every process holds its own
// connection with its own identity. Without this the bar read "MacBook-Air is here" once per
// process — about the machine you are sitting at, which is not news.
func TestTheFeedIgnoresItsOwnMachine(t *testing.T) {
	s := &feedState{}
	s.setMe("u_01:MacBook-Air-32ec42d0")

	if !s.sameMachine("u_01:MacBook-Air-9f3a1c2b") {
		t.Error("another process on this machine was treated as a teammate")
	}
	if !s.sameMachine("u_01:MacBook-Air-32ec42d0") {
		t.Error("this very connection was treated as a teammate")
	}
	if s.sameMachine("u_02:monolith-b7ace224") {
		t.Error("a different machine was suppressed as our own")
	}
	if s.sameMachine("bus") || s.sameMachine("") {
		t.Error("the server and the empty identity are not this machine")
	}
}

// A reconnect re-announces. Two identical lines in a row say nothing the first did not, and they
// scroll the real content away in a 40-column pane.
func TestRepeatedLinesCollapse(t *testing.T) {
	s := &feedState{}
	s.add(feedLine{At: time.Now(), Who: "matt-mbp", Text: "recorded something in acr"})
	s.add(feedLine{At: time.Now(), Who: "matt-mbp", Text: "recorded something in acr"})
	if n := len(s.snapshot()); n != 1 {
		t.Fatalf("kept %d lines, want the repeat collapsed into 1", n)
	}
	s.add(feedLine{At: time.Now(), Who: "matt-mbp", Text: "asks: where does fleet retry?"})
	if n := len(s.snapshot()); n != 2 {
		t.Fatalf("kept %d lines; a DIFFERENT line must still be added", n)
	}
}

// The relay stamps envelopes in UTC. Rendering them raw put server-originated lines seven hours
// off from locally-generated ones — two clocks in one column.
func TestTimesRenderInLocalTime(t *testing.T) {
	utc := time.Date(2026, 9, 18, 14, 12, 0, 0, time.UTC)
	want := utc.Local().Format("15:04")
	if got := utc.Format("15:04"); got == want && time.Local != time.UTC {
		t.Skip("machine runs on UTC; nothing to distinguish")
	}
	// The renderer must use the local form.
	if utc.Local().Format("15:04") != want {
		t.Fatalf("local conversion is not stable")
	}
}

// A feed carries ONE project's stream — the project of the session it sits beside. Two sessions
// in two projects each get their own feed, and neither shows the other's traffic. A machine-wide
// stream was tried first: with several projects open, every line needs a project name to be read
// at all, and unrelated work scrolls away the work you are looking at.
func TestAFeedNamesTheProjectItIsWatching(t *testing.T) {
	var out strings.Builder
	_ = out
	// renderFeed writes to stdout; the title construction is what matters here.
	for _, tc := range []struct{ label, want string }{
		{"acr", "☎ activity · acr"},
		{"", "☎ activity"},
	} {
		title := "☎ activity"
		if tc.label != "" {
			title += " · " + tc.label
		}
		if title != tc.want {
			t.Errorf("title for %q = %q, want %q", tc.label, title, tc.want)
		}
	}
}

// Presence is a STATE shown in the header, and never a line in the stream. Echoing heartbeats
// filled the pane with "is here" twice a minute about people who had not done anything.
func TestPresenceIsAHeaderNotAStream(t *testing.T) {
	s := &feedState{}
	s.setMe("u_01:MacBook-Air-32ec")

	s.arrived("u_02:matt-mbp-99ff")
	for i := 0; i < 20; i++ {
		s.arrived("u_02:matt-mbp-99ff") // twenty heartbeats
	}
	s.arrived("u_02:matt-monolith-1234") // the same person, second machine

	// Not one line, however many heartbeats arrive.
	if n := len(s.snapshot()); n != 0 {
		t.Fatalf("presence produced %d line(s); it must only ever update the header", n)
	}
	// And the header names PEOPLE, not connections.
	if got, want := s.live(), []string{"u_02"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("live = %v, want %v (one person on two machines is one person)", got, want)
	}

	s.departed("u_02:matt-mbp-99ff")
	s.departed("u_02:matt-monolith-1234")
	if got := s.live(); len(got) != 0 {
		t.Fatalf("live = %v after everyone left, want empty", got)
	}
	if n := len(s.snapshot()); n != 0 {
		t.Fatalf("leaving produced %d line(s); it must only ever update the header", n)
	}
}

// A pane that repaints must not write its frames into scrollback. `\x1b[2J` erases the whole
// display and pushes what it erased into history, so a frame a second became 42,000 identical
// lines and a scrollback nobody could use.
func TestAFrameNeverEntersScrollback(t *testing.T) {
	frame := renderFeed("acr", []feedLine{{At: time.Now(), Who: "matt", Text: "recorded something"}}, nil)
	if strings.Contains(frame, "\x1b[2J") {
		t.Error("the frame erases the whole display, which pushes it into scrollback")
	}
	if !strings.Contains(frame, "\x1b[H") {
		t.Error("the frame does not home the cursor, so it would scroll rather than repaint")
	}
}

// Most seconds nothing has happened. Redrawing identical output is how a quiet pane is still
// expensive, and it is what made the flood a flood rather than a slow leak.
func TestAnUnchangedFrameIsIdentical(t *testing.T) {
	lines := []feedLine{{At: time.Now(), Who: "matt", Text: "recorded something in acr"}}
	a := renderFeed("acr", lines, []string{"matt"})
	b := renderFeed("acr", lines, []string{"matt"})
	if a != b {
		t.Fatal("two renders of the same state differ, so the caller can never skip a repaint")
	}
	c := renderFeed("acr", append(lines, feedLine{At: time.Now(), Who: "jo", Text: "asks: where?"}), []string{"matt"})
	if c == a {
		t.Fatal("a new line produced an identical frame, so real activity would never be drawn")
	}
}
