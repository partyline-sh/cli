package main

// The session bus: a WebSocket endpoint on the relay.
//
// WHY HERE. Next.js cannot upgrade a WebSocket inside a route handler, which is why the daemon's
// event stream is SSE polling the database every two seconds. The relay is already a long-lived
// Go process in the stack, already published by the image pipeline, and already verifies callers
// against the control plane — so the bus costs one port and one Caddy route instead of a new
// service to build, ship, monitor and secure.
//
// WHAT IT CARRIES. News, not content: "a fact changed in project X", "this agent is asking that
// one". Receivers fetch from the durable store (git for memory). That split is deliberate — the
// bus may lose a message under load and the system still converges, because git is the truth and
// this is only the fast path to knowing.
//
// The relay stays BLIND to session ciphertext as before; this endpoint is a separate listener
// with its own auth and shares nothing with the splice path.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"partyline.sh/partyline/internal/bus"
	"partyline.sh/partyline/internal/obs"
)

// identity is who the control plane says a token belongs to.
type identity struct {
	Org      string `json:"org"`
	Identity string `json:"identity"`
}

// verifyBus asks the control plane who this token is. Fails CLOSED on any error, exactly like
// verifyHost: an unauthenticated connection on a fan-out bus would be able to read another
// team's notifications.
//
// The machine name is a HINT, forwarded from the client and sanitized by the control plane. It
// does not authenticate anything; it exists because the hub keys connections by identity and
// replaces on reconnect, so one person's laptop and server need to be two participants rather
// than one connection that each keeps stealing from the other.
func verifyBus(token, machine string) (identity, bool) {
	var who identity
	if strings.TrimSpace(token) == "" {
		return who, false
	}
	hint, _ := json.Marshal(map[string]string{"machine": machine})
	base := strings.TrimRight(envOr("RELAY_API", "https://partyline.sh"), "/")
	req, err := http.NewRequest("POST", base+"/api/v1/relay/whoami", bytes.NewReader(hint))
	if err != nil {
		return who, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return who, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return who, false
	}
	if json.NewDecoder(resp.Body).Decode(&who) != nil || who.Org == "" || who.Identity == "" {
		return who, false
	}
	return who, true
}

// busSender is the From on envelopes the server itself originates (rosters, departures, errors),
// so a client can tell them from a teammate's message. It is not a valid identity, and the server
// stamps every client message with the real one, so nothing can impersonate it.
const busSender = "bus"

const (
	busWriteWait = 10 * time.Second
	busPing      = 25 * time.Second
	busOutBuffer = 64
)

// serveBus runs the WebSocket listener. Separate port from the splice listener so the two
// protocols never have to be told apart on one socket.
func serveBus(hub *bus.Hub) {
	addr := ":" + envOr("BUS_PORT", "2223")
	log.Printf("☎ partyline bus (websocket) listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: busHandler(hub), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("relay: bus listener stopped: %v", err)
	}
}

// busHandler is the routing, separated from the listening so a test can drive the real upgrade,
// auth and scoping path over a real socket instead of asserting against the parts.
func busHandler(hub *bus.Hub) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/bus", func(w http.ResponseWriter, r *http.Request) {
		defer obs.Recover()
		who, ok := verifyBus(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Header.Get("X-Machine"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		projects := splitList(r.URL.Query().Get("projects"))
		if len(projects) == 0 {
			http.Error(w, "name at least one project — delivery is scoped", http.StatusBadRequest)
			return
		}
		// InsecureSkipVerify is about the ORIGIN header, not TLS: this endpoint is reached by
		// CLIs, which send no Origin, and it authenticates with a bearer token rather than a
		// cookie — so there is no browser session for a hostile page to ride.
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		serveConn(r.Context(), c, hub, who, projects)
	})
	mux.HandleFunc("/bus/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "connections": hub.Count()})
	})
	return mux
}

// serveConn is one participant's life on the bus: register, pump out, read in.
func serveConn(ctx context.Context, c *websocket.Conn, hub *bus.Hub, who identity, projects []string) {
	conn := &bus.Conn{Identity: who.Identity, Org: who.Org, Projects: projects, Out: make(chan bus.Envelope, busOutBuffer)}
	hub.Add(conn)
	defer c.CloseNow()
	defer func() {
		hub.Remove(conn.Identity)
		// Say who left. Without this a roster stays stale until its entries expire, and the bar
		// would show a teammate as present for a minute after they closed their laptop.
		for _, p := range projects {
			hub.Publish(who.Org, bus.Envelope{Type: bus.TypePresence, Project: p, From: busSender,
				Payload: json.RawMessage(`{"left":` + quote(who.Identity) + `}`), At: time.Now()})
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Writer: also the pinger, because a silent connection through a proxy is indistinguishable
	// from a dead one and both ends need to find out.
	go func() {
		defer obs.Recover()
		t := time.NewTicker(busPing)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case env, open := <-conn.Out:
				if !open {
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, busWriteWait)
				err := wsjson.Write(wctx, c, env)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, busWriteWait)
				err := c.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Announce arrival so a fleet view is live rather than polled, and hand the arriver the
	// roster of who is ALREADY here. Without the roster, a joiner learns about existing peers
	// only when one of them next says something, so the bar would read "nobody here" while two
	// teammates sat connected.
	for _, p := range projects {
		hub.Publish(who.Org, bus.Envelope{Type: bus.TypePresence, Project: p, From: who.Identity, At: time.Now()})
		// "you" is how a client learns the identity the control plane assigned it. It cannot
		// derive that itself — it sends a machine hint and the server decides — and without it a
		// client cannot tell its own entry in the roster from a teammate's.
		roster, _ := json.Marshal(map[string]any{"peers": hub.Peers(who.Org, p), "you": who.Identity})
		select {
		case conn.Out <- bus.Envelope{Type: bus.TypePresence, Project: p, From: busSender, Payload: roster, At: time.Now()}:
		default:
		}
	}

	for {
		var env bus.Envelope
		if err := wsjson.Read(ctx, c, &env); err != nil {
			return
		}
		// The sender is whoever the control plane said this token is — never what the message
		// claims. A client-supplied From would let any connection impersonate a teammate.
		env.From = who.Identity
		env.At = time.Now()
		if err := env.Validate(); err != nil {
			_ = wsjson.Write(ctx, c, bus.Envelope{Type: bus.TypePresence, Project: env.Project, From: busSender,
				Payload: json.RawMessage(`{"error":` + quote(err.Error()) + `}`), At: time.Now()})
			continue
		}
		if !conn.Subscribed(env.Project) {
			continue // a connection may only publish into a project it declared
		}
		hub.Publish(who.Org, env)
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
