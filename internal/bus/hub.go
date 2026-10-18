package bus

import (
	"sync"
	"time"
)

// Hub is the fan-out: who is connected, and who should see a given message.
//
// In memory, per process, single node — and that is a real limit, stated rather than discovered:
// two relay instances behind a load balancer would each only reach their own half of the
// connections. The fix when it is needed is a shared pub/sub between instances, not a bigger
// map; until a second instance exists, adding one would be infrastructure serving nobody.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*Conn // identity → connection
}

// Conn is one live participant: a CLI, a session, a teammate's machine.
type Conn struct {
	// Identity is server-assigned and unique. Everything that addresses a peer uses it.
	Identity string
	// Org scopes who may ever see whom. A message never crosses it, whatever it claims.
	Org string
	// Projects the connection subscribed to.
	Projects []string
	// Out carries messages to the socket writer. BUFFERED, and a full buffer DROPS rather than
	// blocks: one stalled client must never be able to freeze the fan-out for everyone else,
	// which is the classic way a chat bus takes down a server.
	Out      chan Envelope
	LastSeen time.Time
}

func NewHub() *Hub { return &Hub{conns: map[string]*Conn{}} }

// Add registers a connection. Replacing an identity closes the old one: a reconnect after a
// dropped socket must not leave a zombie receiving half the traffic.
func (h *Hub) Add(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[c.Identity]; ok {
		close(old.Out)
	}
	c.LastSeen = time.Now()
	h.conns[c.Identity] = c
}

func (h *Hub) Remove(identity string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.conns[identity]; ok {
		delete(h.conns, identity)
		close(c.Out)
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Publish fans a message out and reports how many connections took it. Only within the sender's
// org, only to subscribers of the message's project, never back to the sender.
func (h *Hub) Publish(org string, e Envelope) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, c := range h.conns {
		if c.Org != org || !c.Subscribed(e.Project) {
			continue
		}
		if !e.Deliver(c.Identity, e.Project) {
			continue
		}
		select {
		case c.Out <- e:
			n++
		default:
			// Dropped: this client is not reading. It will re-sync from git on its next
			// interval, which is exactly why the bus is allowed to lose a message and the
			// durable store is not.
		}
	}
	return n
}

// Peers lists who else is on the bus for a project — what a fleet view asks for.
func (h *Hub) Peers(org, project string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for _, c := range h.conns {
		if c.Org == org && c.Subscribed(project) {
			out = append(out, c.Identity)
		}
	}
	return out
}

// Subscribed reports whether this connection declared the project. Exported because the server
// checks it on PUBLISH too: a connection may only send into a project it subscribed to.
func (c *Conn) Subscribed(project string) bool {
	for _, p := range c.Projects {
		if p == project {
			return true
		}
	}
	return false
}
