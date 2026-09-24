package main

// Layer 3 of memory delivery: the bus ACCELERATES the watcher, it never replaces it.
//
// Layer 1 is the local poke (a write on this machine redraws this machine's bar immediately).
// Layer 2 is the watcher's 45-second fetch, which is what makes a teammate's fact eventually
// arrive no matter what. Layer 3 is this file: when the bus is up, "eventually" becomes "now".
//
// The ordering rule that keeps this safe: the bus carries NEWS, never CONTENT. A memory.changed
// envelope says only that project X moved; the receiver still fetches from git, which is the
// truth. So a dropped envelope costs latency, not correctness — layer 2 collects it on the next
// tick — and a forged one costs a pointless fetch. Nothing here may become load-bearing.

import (
	"context"
	"encoding/json"
	"time"

	"partyline.sh/partyline/internal/bus"
)

// announceTimeout bounds the send. Generous, because nothing waits on the outcome: the fact is
// already committed and pushed before this runs, and a teammate whose notification never arrives
// still gets the fact from the watcher's fetch. The ceiling only exists so an unreachable
// instance cannot hang a command.
const announceTimeout = 3 * time.Second

// announceMemoryChanged tells the project that its memory moved.
//
// SYNCHRONOUS, and that is the whole point. This used to hand the send to a goroutine and return
// immediately — which works in a long-lived process and never works in a short one. `ptln memory
// add` printed its confirmation and exited microseconds later, killing the dial mid-handshake,
// so the announcement was never sent ONCE from any CLI write since the bus shipped. The bus was
// healthy the entire time; nothing was ever published into it.
//
// The cost of waiting is a few hundred milliseconds on a command that has just done a git push,
// and the message is informational — it buys a teammate news in about a second instead of within
// the watcher's interval. Nothing anyone can do depends on it arriving.
func announceMemoryChanged(label string) {
	if label == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
	defer cancel()
	_ = busSend(ctx, label, bus.TypeMemoryChanged, "", map[string]string{"project": label})
}

// watchBus fetches on news instead of on the clock. Runs alongside the interval loop, which
// keeps ticking: if the bus is down, unreachable or never configured, the only difference is
// that a teammate's fact lands within the interval rather than within a second.
func watchBus(ctx context.Context) {
	var labels []string
	byLabel := map[string]string{}
	for _, ws := range allWorkspaces() {
		labels = append(labels, ws.Label)
		byLabel[ws.Label] = ws.Dir
	}
	if len(labels) == 0 {
		return
	}
	// One tracker per project, written to disk for the status bar to read. See presence.go for
	// why the bar reads a file instead of the socket.
	trackers := map[string]*presenceTracker{}
	for _, l := range labels {
		trackers[l] = newPresenceTracker()
		clearPresence(l) // a roster from a previous run is not evidence about this one
	}

	cl := runBusClient(ctx, labels, func(e bus.Envelope) { onBusEvent(e, trackers, byLabel) })

	// Re-announce, so a teammate who connects after us still learns we are here, and so a peer
	// who missed our arrival expires us rather than showing us forever. Sent on the client's own
	// connection: a second dial would carry this process's identity and evict its own listener.
	go func() {
		t := time.NewTicker(presenceBeat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, l := range labels {
					cl.Send(l, bus.TypePresence, map[string]string{})
					writePresence(l, trackers[l].live())
				}
			}
		}
	}()
	defer func() {
		for _, l := range labels {
			clearPresence(l)
		}
	}()

	<-ctx.Done()
}

// onBusEvent applies one envelope to local state: presence rosters to the file the status bar
// reads, memory news to a fetch.
func onBusEvent(e bus.Envelope, trackers map[string]*presenceTracker, byLabel map[string]string) {
	tr := trackers[e.Project]
	switch e.Type {
	case bus.TypePresence:
		if tr == nil {
			return
		}
		// The server originates rosters and departures; everything else is a peer saying it is here.
		var body struct {
			Peers []string `json:"peers"`
			Left  string   `json:"left"`
			You   string   `json:"you"`
		}
		_ = json.Unmarshal(e.Payload, &body)
		if body.You != "" {
			tr.self(body.You)
		}
		for _, id := range body.Peers {
			tr.saw(id)
		}
		if body.Left != "" {
			tr.left(body.Left)
		}
		tr.saw(e.From)
		writePresence(e.Project, tr.live())
		pokeStatus()
	case bus.TypeMemoryChanged:
		dir, ok := byLabel[e.Project]
		if !ok {
			return
		}
		if memoryFetchMoved(dir) {
			pokeStatus()
		}
	}
}
