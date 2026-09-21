package main

// Who else is working on this project right now.
//
// The status bar redraws on tmux's interval and must never touch the network, so presence is
// written to a file by the one process that already holds a bus connection (the memory watcher)
// and READ by the status command. That split is the whole design: the bar stays instant, and the
// network work happens in a process that is allowed to be slow.
//
// Presence is a nicety, not a fact. It goes stale, it is wrong for a few seconds after someone
// disconnects ungracefully, and it is simply absent when the bus is down. Nothing may depend on
// it — a teammate who does not appear here may well be working.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// presenceTTL is how long a peer stays listed without being heard from. Longer than the client's
// re-announce interval, so an ordinary quiet period does not flicker someone out of the bar.
const presenceTTL = 90 * time.Second

// presenceBeat is how often a connected machine re-announces itself.
const presenceBeat = 30 * time.Second

func presenceDir() string { return filepath.Join(stateDir(), "presence") }

func presencePath(label string) string {
	return filepath.Join(presenceDir(), strings.ReplaceAll(label, string(os.PathSeparator), "_")+".json")
}

// presenceFile is what the watcher writes and the status line reads.
type presenceFile struct {
	Peers map[string]time.Time `json:"peers"` // identity → last heard from
	At    time.Time            `json:"at"`    // when this machine last had a live connection
}

// presenceTracker accumulates what the bus says about one project.
type presenceTracker struct {
	mu    sync.Mutex
	peers map[string]time.Time
	me    string // this machine's identity, as the server assigned it
}

func newPresenceTracker() *presenceTracker {
	return &presenceTracker{peers: map[string]time.Time{}}
}

// self records the identity the server assigned this connection, so this machine is never
// counted among the teammates on its own status bar. The status command runs in a different
// process and cannot work this out for itself.
func (p *presenceTracker) self(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.me = id
	delete(p.peers, id)
}

// saw records that an identity is present now.
func (p *presenceTracker) saw(id string) {
	if id == "" || id == "bus" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == p.me {
		return
	}
	p.peers[id] = time.Now()
}

// left drops an identity that announced its departure.
func (p *presenceTracker) left(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.peers, id)
}

// live is the identities heard from within the TTL, sorted so the file does not churn.
func (p *presenceTracker) live() map[string]time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]time.Time{}
	for id, at := range p.peers {
		if time.Since(at) < presenceTTL {
			out[id] = at
		}
	}
	return out
}

// writePresence persists a project's roster. Best-effort: a status bar without presence is a
// smaller problem than a watcher that dies trying to write one.
func writePresence(label string, peers map[string]time.Time) {
	if err := os.MkdirAll(presenceDir(), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(presenceFile{Peers: peers, At: time.Now()})
	if err != nil {
		return
	}
	tmp := presencePath(label) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	// Rename rather than truncate-and-write: the status line reads this file on every redraw and
	// must never catch it half-written.
	_ = os.Rename(tmp, presencePath(label))
}

// clearPresence removes a project's roster — used when the bus connection drops, because a stale
// file would keep claiming teammates are present long after this machine stopped hearing from them.
func clearPresence(label string) { _ = os.Remove(presencePath(label)) }

// readPresence is what the status line calls: the peers other than this machine, or nothing at
// all when the file is missing, stale or unreadable. Never returns an error — a bar has nowhere
// to put one.
func readPresence(label string) []string {
	b, err := os.ReadFile(presencePath(label))
	if err != nil {
		return nil
	}
	var pf presenceFile
	if json.Unmarshal(b, &pf) != nil {
		return nil
	}
	// A file older than the TTL means the watcher stopped updating it (crashed, or the bus went
	// away). Report nothing rather than a roster nobody is maintaining.
	if time.Since(pf.At) > presenceTTL {
		return nil
	}
	var out []string
	for id, at := range pf.Peers {
		if time.Since(at) > presenceTTL {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// peopleOf collapses identities to the humans behind them. An identity is "<user>:<machine>", so
// one person with a laptop and a server is one person present, not two.
func peopleOf(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		who := id
		if i := strings.IndexByte(id, ':'); i > 0 {
			who = id[:i]
		}
		if !seen[who] {
			seen[who] = true
			out = append(out, who)
		}
	}
	sort.Strings(out)
	return out
}
