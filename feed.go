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
// It subscribes to every project on this machine, not just the one in the pane's directory: the
// point is to see your teammates without going looking, and a feed scoped to whichever repo you
// happened to open would be silent most of the time.

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
	mu    sync.Mutex
	lines []feedLine
}

func (s *feedState) add(l feedLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		var p struct {
			Left  string   `json:"left"`
			Peers []string `json:"peers"`
			You   string   `json:"you"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		if p.Left != "" {
			return "left " + e.Project
		}
		// A roster or our own heartbeat is bookkeeping, not activity.
		if p.You != "" || len(p.Peers) > 0 {
			return ""
		}
		return "is here"
	}
	return ""
}

// feedMain is `ptln feed`: the pane beside the agent.
func feedMain() {
	st := &feedState{}
	labels := map[string]bool{}
	var subs []string
	for _, ws := range allWorkspaces() {
		if !labels[ws.Label] {
			labels[ws.Label], subs = true, append(subs, ws.Label)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(subs) == 0 {
		st.add(feedLine{At: time.Now(), Who: "partyline", Text: "no project on this machine yet"})
	} else {
		runBusClient(ctx, subs, func(e bus.Envelope) {
			if t := feedText(e); t != "" {
				st.add(feedLine{At: e.At, Who: shortWho(e.From), Text: t, Kind: e.Type})
			}
		})
		st.add(feedLine{At: time.Now(), Who: "partyline", Text: "watching " + strings.Join(subs, ", ")})
	}

	// Repaint on a slow tick rather than per event: a burst of arrivals should cost one redraw,
	// and a pane nobody is looking at should cost nothing much either.
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		renderFeed(st.snapshot())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// renderFeed paints the pane. Newest at the BOTTOM, like a chat log and like every terminal
// people already read, so the eye lands where the new thing is.
func renderFeed(lines []feedLine) {
	w, h := termSize()
	if w < 12 {
		w = 12
	}
	amber, pill, dim := brand.Fg(brand.AmberRGB), brand.Fg(brand.PillRGB), "\x1b[38;5;245m"
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J") // home, clear
	fmt.Fprintf(&b, "%s☎ activity\x1b[0m\n%s%s\x1b[0m\n", amber, dim, strings.Repeat("─", w))

	body := h - 3
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
		head := fmt.Sprintf("%s %s", l.At.Format("15:04"), l.Who)
		fmt.Fprintf(&b, "%s%s\x1b[0m\n", color, clipVis(head, w))
		for _, wrapped := range wrapPlain(l.Text, w-2) {
			fmt.Fprintf(&b, "  %s\n", clipVis(wrapped, w-2))
		}
	}
	fmt.Print(b.String())
}
