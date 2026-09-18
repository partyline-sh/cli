package main

// THE ACTIVITY FEED — what everyone else on the project is doing, beside your agent.
//
// Everything partyline knows has so far been invisible until it spoke inside a session, which is
// right for the agent and wrong for the person. The status row answered "what is the state of
// this project"; this answers "what is happening right now, and who else is here".
//
// It is a READER. The feed never writes to the bus and never changes anything — closing it loses
// nothing, which is what lets it be a thing you flick on and off rather than a mode you commit
// to.
//
// ONE FEED, ONE PROJECT. The feed is a pane beside a particular agent working in a particular
// project, and it carries that project's stream. Two sessions in two projects each get their own
// feed showing only their own traffic.
//
// The alternative — one machine-wide stream — was tried first and is wrong: with several projects
// open, every line needs a project name to be読 read at all, unrelated work scrolls away the work
// you are looking at, and "is here" from a project you are not touching is noise. Scoping costs a
// quiet pane sometimes, which is the correct failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/bus"
)

// feedMax is how many lines are kept. A feed is a window on now, not a log — the durable record
// is the memory repo, and anything worth keeping is a fact rather than a line here.
const feedMax = 300

type feedLine struct {
	At   time.Time
	Who  string
	Text string
	Kind bus.Type
}

type feedState struct {
	mu      sync.Mutex
	lines   []feedLine
	me      string          // this connection's identity, as the server assigned it
	present map[string]bool // who we have already reported as here
}

// arrived reports whether this identity is NEWS — someone we had not already seen. Presence is
// re-announced every 30 seconds by every connected process, so echoing each one filled the pane
// with "is here" twice a minute, forever, about people who had not done anything. A feed shows
// CHANGES; the standing count of who is around belongs in the ribbon, where it costs no lines.
func (s *feedState) arrived(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present == nil {
		s.present = map[string]bool{}
	}
	if s.present[id] {
		return false
	}
	s.present[id] = true
	return true
}

// departed forgets someone, so a later return counts as news again.
func (s *feedState) departed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.present[id] {
		return false
	}
	delete(s.present, id)
	return true
}

// live is the people currently connected, collapsed from identities: one person with a laptop
// and a server is one person here, not two.
func (s *feedState) live() []string {
	s.mu.Lock()
	ids := make([]string, 0, len(s.present))
	for id := range s.present {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	return peopleOf(ids)
}

// setMe records who we are, so the feed stops reporting this machine's own comings and goings.
// A reconnect announces a departure for the identity that just left, and rendering it read as
// "partyline left acr" — the feed narrating its own socket.
func (s *feedState) setMe(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.me = id
}

// sameMachine reports whether an identity belongs to the machine this feed runs on. Identities
// carry a per-process suffix, so a string match would only catch this one process; comparing the
// machine name catches every process here, which is what "not news" means.
func (s *feedState) sameMachine(id string) bool {
	s.mu.Lock()
	me := s.me
	s.mu.Unlock()
	if id == "" || me == "" || id == "bus" {
		return false
	}
	return shortWho(id) == shortWho(me)
}

func (s *feedState) add(l feedLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A reconnect re-announces, and two identical lines in a row tell you nothing the first did
	// not. Collapse rather than scroll the real content away.
	if n := len(s.lines); n > 0 && s.lines[n-1].Who == l.Who && s.lines[n-1].Text == l.Text {
		s.lines[n-1].At = l.At
		return
	}
	s.lines = append(s.lines, l)
	if len(s.lines) > feedMax {
		s.lines = s.lines[len(s.lines)-feedMax:]
	}
}

func (s *feedState) snapshot() []feedLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]feedLine(nil), s.lines...)
}

// shortWho turns "u_01H8…:darcy-mbp-9f3a" into "darcy-mbp". An identity is built for routing,
// not for reading, and a column 40 wide has no room for either half in full.
func shortWho(id string) string {
	if id == "" || id == "bus" {
		return "partyline"
	}
	who := id
	if i := strings.IndexByte(who, ':'); i >= 0 {
		who = who[i+1:]
	}
	if i := strings.LastIndexByte(who, '-'); i > 0 && len(who)-i <= 9 {
		who = who[:i] // drop the per-process suffix
	}
	return who
}

// feedText renders one envelope as a line a person reads, or "" for events not worth a line.
func feedText(e bus.Envelope) string {
	switch e.Type {
	case bus.TypeMemoryChanged:
		return "recorded something in " + e.Project
	case bus.TypeAsk:
		var p struct{ Text string }
		_ = json.Unmarshal(e.Payload, &p)
		if p.Text != "" {
			return "asks: " + p.Text
		}
		return "asked something"
	case bus.TypeAnswer:
		var p struct{ Text string }
		_ = json.Unmarshal(e.Payload, &p)
		if p.Text != "" {
			return "answers: " + p.Text
		}
		return "answered"
	case bus.TypePresence:
		// Presence is handled as a state transition by the caller — a heartbeat arrives every
		// 30 seconds from every connected process and is never, by itself, a line.
		return ""
	}
	return ""
}

// feedMain is `ptln feed [--project <label>]`: the pane beside the agent.
//
// The project comes from the pane this was opened beside — resolved by the toggle and passed
// explicitly, so the feed does not depend on inheriting a working directory.
func feedMain(args []string) {
	label := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" && i+1 < len(args) {
			label, i = args[i+1], i+1
		}
	}
	if label == "" {
		cwd, _ := os.Getwd()
		if ws, ok := projectForDir(cwd); ok {
			label = ws.Label
		}
	}

	st := &feedState{}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if label == "" {
		// Say which question cannot be answered, rather than showing an empty box.
		st.add(feedLine{At: time.Now(), Who: "partyline", Text: "this session is not in a project, so there is no stream to follow"})
	} else {
		st.add(feedLine{At: time.Now(), Who: "partyline", Text: "watching " + label})
		runBusClient(ctx, []string{label}, func(e bus.Envelope) {
			// The server tells a new connection which identity it was given. Without it the feed
			// cannot tell its own comings and goings from a teammate's, and narrated both.
			if e.Type == bus.TypePresence {
				var body struct {
					You string `json:"you"`
				}
				if json.Unmarshal(e.Payload, &body) == nil && body.You != "" {
					st.setMe(body.You)
				}
			}
			// Presence about THIS MACHINE is not news. Every process here holds its own
			// connection — the memory watcher, this feed — so without this the bar read
			// "MacBook-Air is here" once per process, about the machine you are sitting at.
			if e.Type == bus.TypePresence && st.sameMachine(e.From) {
				return
			}
			// PRESENCE IS A HEADER, NOT A STREAM. Every connected process re-announces every
			// 30 seconds, so echoing it filled the pane with "is here" twice a minute about
			// people who had not done anything. Who is around is a STATE — it belongs at the
			// top where it costs one line no matter how long anyone stays. This pane is for
			// things that happened.
			if e.Type == bus.TypePresence {
				var body struct {
					Left  string   `json:"left"`
					Peers []string `json:"peers"`
				}
				_ = json.Unmarshal(e.Payload, &body)
				switch {
				case body.Left != "":
					st.departed(body.Left)
				case len(body.Peers) > 0:
					for _, p := range body.Peers {
						if !st.sameMachine(p) {
							st.arrived(p)
						}
					}
				default:
					if !st.sameMachine(e.From) {
						st.arrived(e.From)
					}
				}
				return
			}
			if t := feedText(e); t != "" {
				st.add(feedLine{At: e.At, Who: shortWho(e.From), Text: t, Kind: e.Type})
			}
		})
	}

	// THE ALTERNATE SCREEN. A full-screen pane that repaints belongs here for the same reason
	// every editor and pager does: the alternate screen has no scrollback, so a frame cannot
	// become history. Without it the feed wrote a screenful into the pane's history every
	// second — tens of thousands of identical lines, and a scrollback nobody could use.
	// Restored on the way out so the shell underneath comes back untouched.
	fmt.Print("\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	defer fmt.Print("\x1b[?25h\x1b[?1049l")

	// Repaint on a slow tick rather than per event: a burst of arrivals should cost one redraw.
	// An UNCHANGED frame costs nothing at all — most seconds nothing has happened, and redrawing
	// the same pixels is how a quiet pane still manages to be expensive.
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := ""
	for {
		if frame := renderFeed(label, st.snapshot(), st.live()); frame != last {
			fmt.Print(frame)
			last = frame
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// renderFeed builds the frame. Newest at the BOTTOM, like a chat log and like every terminal
// people already read, so the eye lands where the new thing is.
//
// It RETURNS the frame rather than printing it, so the caller can compare against the last one
// and skip a repaint that would change nothing.
func renderFeed(label string, lines []feedLine, live []string) string {
	w, h := termSize()
	if w < 12 {
		w = 12
	}
	amber, pill, dim := brand.Fg(brand.AmberRGB), brand.Fg(brand.PillRGB), "\x1b[38;5;245m"
	var b strings.Builder
	// Home, then erase forward. NOT \x1b[2J: erasing the whole display pushes what it erased
	// into scrollback, so a frame a second became 42,000 lines of identical history in a pane
	// nobody could then scroll. \x1b[J from the home position clears to the end of the screen
	// without touching history, and the alternate screen below means there is no history anyway.
	b.WriteString("\x1b[H\x1b[J")
	title := "☎ activity"
	if label != "" {
		title += " · " + label
	}
	fmt.Fprintf(&b, "%s%s\x1b[0m\n", amber, clipVis(title, w))
	// Who is here, as a standing line. Silent when you are working alone, so an empty project
	// does not carry a permanent reminder that it is empty.
	header := 2
	if len(live) > 0 {
		fmt.Fprintf(&b, "%s%s\x1b[0m\n", dim, clipVis("◉ "+strings.Join(live, ", "), w))
		header = 3
	}
	fmt.Fprintf(&b, "%s%s\x1b[0m\n", dim, strings.Repeat("─", w))

	body := h - header - 1
	if body < 1 {
		body = 1
	}
	if len(lines) > body {
		lines = lines[len(lines)-body:]
	}
	for _, l := range lines {
		color := dim
		switch l.Kind {
		case bus.TypeMemoryChanged:
			color = amber
		case bus.TypeAsk, bus.TypeAnswer:
			color = pill
		}
		head := fmt.Sprintf("%s %s", l.At.Local().Format("15:04"), l.Who)
		fmt.Fprintf(&b, "%s%s\x1b[0m\n", color, clipVis(head, w))
		for _, wrapped := range wrapPlain(l.Text, w-2) {
			fmt.Fprintf(&b, "  %s\n", clipVis(wrapped, w-2))
		}
	}
	return b.String()
}
