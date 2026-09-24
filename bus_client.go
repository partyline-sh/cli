package main

// The CLI's end of the session bus.
//
// One connection per machine, shared by everything on it: the memory watcher, the status bar,
// and any agent that wants to reach a teammate's agent. One socket rather than one per session,
// because a dozen agent sessions on a laptop should not be a dozen sockets through someone's
// corporate proxy.
//
// It is ALWAYS optional. Every feature that uses the bus keeps working without it — memory still
// syncs on its interval, the bar still refreshes, asks still fall back to polling. That is the
// rule this code enforces by construction: no error here is fatal, and nothing waits on a
// connection that may never come.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/bus"
)

// busURL is where the bus lives. Derived from the instance's base URL, because the bus is part
// of the instance the CLI is already signed in to; overridable for a split deployment.
func busURL(projects []string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("PARTYLINE_BUS")), "/")
	if base == "" {
		base = strings.TrimRight(api.Base(), "/") + "/bus"
	}
	u := strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1)
	return u + "?projects=" + url.QueryEscape(strings.Join(projects, ","))
}

// machineHint names this PROCESS, not just this machine, and that distinction is load-bearing.
//
// The hub keys connections by identity and replaces on reconnect — correct for a client whose
// laptop lid closed, wrong if two processes on one machine share an identity. They do: the memory
// watcher holds a listening connection while `ptln memory add` opens its own to announce a write.
// With a bare hostname, every announcement would evict the watcher that is meant to hear it, and
// nothing would report an error — the machine would just quietly stop receiving news.
//
// Stable for the life of the process, so a reconnect still replaces this process's own stale
// connection rather than accumulating ghosts.
var machineHint = sync.OnceValue(func() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "machine"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i] // "darcy-mbp.local" is the same machine as "darcy-mbp"
	}
	// The control plane caps the hint at 40 characters. Leave room for the suffix, or a long
	// hostname truncates the part that makes this unique and the collision comes straight back.
	if len(host) > 24 {
		host = host[:24]
	}
	var n [4]byte
	if _, err := rand.Read(n[:]); err != nil {
		return host
	}
	return host + "-" + hex.EncodeToString(n[:])
})

// dialBus opens the connection. Returns a closed-over reader; the caller decides what to do with
// each envelope.
func dialBus(ctx context.Context, projects []string) (*websocket.Conn, error) {
	tok := api.LoadToken()
	if tok == "" {
		return nil, fmt.Errorf("not signed in — the bus is the instance's live channel (`ptln login`)")
	}
	c, resp, err := websocket.Dial(ctx, busURL(projects), &websocket.DialOptions{
		HTTPHeader: map[string][]string{
			"Authorization": {"Bearer " + tok},
			"X-Machine":     {machineHint()},
		},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == 401 {
			return nil, fmt.Errorf("the instance refused the connection — try `ptln login`")
		}
		return nil, err
	}
	return c, nil
}

// busClient is ONE connection, read and written by the same process.
//
// It exists because a second connection would not be a second participant: the hub keys by
// identity and replaces, so a process that dialed again to send something would evict its own
// listener. Anything long-lived therefore holds a client and sends through it.
type busClient struct {
	send chan bus.Envelope
}

// runBusClient dials, reconnects with backoff, delivers to fn and writes what Send queues. It
// returns immediately; the connection lives for as long as ctx does.
//
// Backoff matters more here than it looks: a laptop closes its lid, a proxy times out an idle
// socket, an instance restarts during a deploy. None of those should need a human, and none of
// them should turn into a reconnect loop hammering the server.
func runBusClient(ctx context.Context, projects []string, fn func(bus.Envelope)) *busClient {
	cl := &busClient{send: make(chan bus.Envelope, 32)}
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			c, err := dialBus(ctx, projects)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			backoff = time.Second
			cl.pump(ctx, c, fn)
			c.CloseNow()
		}
	}()
	return cl
}

// pump runs one connection's life: a writer draining Send, and this goroutine reading.
func (cl *busClient) pump(ctx context.Context, c *websocket.Conn, fn func(bus.Envelope)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case env := <-cl.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := wsjson.Write(wctx, c, env)
				wcancel()
				if err != nil {
					cancel() // drop the connection; the outer loop redials
					return
				}
			}
		}
	}()
	for ctx.Err() == nil {
		var env bus.Envelope
		if err := wsjson.Read(ctx, c, &env); err != nil {
			return
		}
		fn(env)
	}
}

// Send queues an envelope. Drops rather than blocks: the caller is usually a status heartbeat or
// a change announcement, and neither is worth stalling real work for. A dropped announcement
// costs latency, never a fact — the watcher's interval still collects it.
func (cl *busClient) Send(project string, t bus.Type, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	select {
	case cl.send <- bus.Envelope{Type: t, Project: project, Payload: raw}:
	default:
	}
}

// busSend publishes one envelope from a SHORT-LIVED process — `ptln memory add`, an MCP server
// recording a fact — and returns. A process that also listens must use a busClient instead, or
// this dial will evict its own listener.
func busSend(ctx context.Context, project string, t bus.Type, to string, payload any) error {
	c, err := dialBus(ctx, []string{project})
	if err != nil {
		return err
	}
	defer c.CloseNow()
	raw, _ := json.Marshal(payload)
	return wsjson.Write(ctx, c, bus.Envelope{Type: t, Project: project, To: to, Payload: raw})
}

// busScope is the key this machine subscribes under. A repo declares its shared context in
// `.partyline.json`, so two people working the same project land in the same scope without
// configuring anything. Overridable for the multi-repo case, where one scope spans several repos.
func busScope() string {
	if p := strings.TrimSpace(os.Getenv("PARTYLINE_BUS_PROJECT")); p != "" {
		return p
	}
	dir, _ := os.Getwd()
	return loadRepoBind(dir)
}

// busMain is `ptln bus` — listen, send, or check. Mostly a diagnostic: the bus is meant to be
// used by the watcher and the agents, not typed at.
func busMain(args []string) {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	scope := busScope()
	if scope == "" {
		fatal(fmt.Errorf("this repo has no shared context to listen on — bind one with `ptln thread bind <id>`"))
	}
	ctx := context.Background()
	switch sub {
	case "listen":
		fmt.Printf("listening on %s (ctrl-c to stop)\n", scope)
		runBusClient(ctx, []string{scope}, func(e bus.Envelope) {
			fmt.Printf("%s  %-14s from %s  %s\n", e.At.Format("15:04:05"), e.Type, e.From, strings.TrimSpace(string(e.Payload)))
		})
		<-ctx.Done()
	case "send":
		if len(args) < 1 {
			fatal(fmt.Errorf("usage: ptln bus send \"<message>\" [--to <identity>]"))
		}
		to := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--to" && i+1 < len(args) {
				to = args[i+1]
			}
		}
		if err := busSend(ctx, scope, bus.TypeAsk, to, map[string]string{"text": args[0]}); err != nil {
			fatal(err)
		}
		fmt.Println("sent")
	default:
		fatal(fmt.Errorf("ptln bus: listen | send"))
	}
}
