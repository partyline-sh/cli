// partyline relay — a BLIND rendezvous for end-to-end-encrypted sessions.
// Hosts dial out and register a join code (ownership verified via the control
// plane); joiners dial in with the code; the relay splices ciphertext between
// them over a yamux stream and NEVER decrypts. It holds no key and cannot read
// or modify a session — it sees only the routing code + connection metadata.
// (Replaces the old SSH-terminating, plaintext-visible broker.)
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"partyline.sh/partyline/internal/obs"
	"partyline.sh/partyline/internal/wormhole"
)

// verifyHost asks the control plane whether `token` owns the live session for
// `code`. Fails CLOSED on any error.
func verifyHost(code, token string) bool {
	if token == "" {
		return false
	}
	base := strings.TrimRight(envOr("RELAY_API", "https://partyline.sh"), "/")
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequest("POST", base+"/api/v1/relay/register", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// registry maps a join code → the host's yamux session (the relay opens one
// stream per joiner on it).
type registry struct {
	mu    sync.Mutex
	hosts map[string]*yamux.Session
}

func (r *registry) add(code string, s *yamux.Session) {
	r.mu.Lock()
	r.hosts[code] = s
	r.mu.Unlock()
}
func (r *registry) get(code string) *yamux.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hosts[code]
}
func (r *registry) remove(code string, s *yamux.Session) {
	r.mu.Lock()
	if r.hosts[code] == s {
		delete(r.hosts, code)
	}
	r.mu.Unlock()
}

func main() {
	defer obs.Init("relay")()
	addr := ":" + envOr("PORT", "2222")
	reg := &registry{hosts: map[string]*yamux.Session{}}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("relay: listen %s: %v", addr, err)
	}
	log.Printf("☎ partyline relay (E2EE blind splice) listening on %s", addr)
	go reportHealth() // pool-director heartbeat (no-op unless RELAY_ID + RELAY_SECRET set)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(c, reg)
	}
}

// reportHealth pings the control plane every 30s so the relay-pool director
// (assign_relay) knows this instance is alive — it drops relays stale >90s. No-op
// unless RELAY_ID + RELAY_SECRET are set, so dev/local relays stay silent.
func reportHealth() {
	id := strings.TrimSpace(os.Getenv("RELAY_ID"))
	secret := strings.TrimSpace(os.Getenv("RELAY_SECRET"))
	if id == "" || secret == "" {
		return
	}
	base := strings.TrimRight(envOr("RELAY_API", "https://partyline.sh"), "/")
	body, _ := json.Marshal(map[string]string{"relay_id": id})
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		req, err := http.NewRequest("POST", base+"/api/v1/relay/heartbeat", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Relay-Secret", secret)
			if resp, derr := (&http.Client{Timeout: 8 * time.Second}).Do(req); derr == nil {
				resp.Body.Close()
			}
		}
		<-t.C
	}
}

func handle(c net.Conn, reg *registry) {
	defer obs.Recover() // a panic in one conn must not crash the relay
	// NOTE: no per-IP rate limiter here — the CPLN Direct LB does not preserve
	// client IPs, so a per-IP limit is effectively a global one and an internet
	// port-scanner on :22 would starve real users. Brute-force is instead bounded
	// by high-entropy codes + the E2EE requirement (a joiner needs the link KEY,
	// not just the code, to read anything). Proper abuse protection (PROXY-protocol
	// real IPs, or rate-limiting failed code lookups) is a pre-launch follow-up.
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second)) // header deadline
	role, ctrl, err := wormhole.ReadHeader(c)
	if err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	switch role {
	case wormhole.RoleHost:
		handleHost(c, ctrl, reg)
	case wormhole.RoleJoiner:
		handleJoiner(c, ctrl, reg)
	default:
		c.Close()
	}
}

func yamuxCfg() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.LogOutput = io.Discard
	return cfg
}

// handleHost: verify ownership, then run a yamux CLIENT on the conn (the relay
// opens streams to the host, one per joiner). Block until the session dies.
func handleHost(c net.Conn, ctrl []byte, reg *registry) {
	var hc wormhole.HostCtrl
	if json.Unmarshal(ctrl, &hc) != nil || hc.Code == "" {
		_ = wormhole.WriteAck(c, false)
		c.Close()
		return
	}
	if !verifyHost(hc.Code, hc.Token) {
		log.Printf("host register DENIED code=%s (ownership check failed)", hc.Code)
		_ = wormhole.WriteAck(c, false)
		c.Close()
		return
	}
	if wormhole.WriteAck(c, true) != nil {
		c.Close()
		return
	}
	sess, err := yamux.Client(c, yamuxCfg()) // relay opens streams → relay is client
	if err != nil {
		c.Close()
		return
	}
	reg.add(hc.Code, sess)
	log.Printf("host registered code=%s", hc.Code)
	<-sess.CloseChan() // host gone (conn closed)
	reg.remove(hc.Code, sess)
	_ = sess.Close()
	log.Printf("host unregistered code=%s", hc.Code)
}

// handleJoiner: look up the host by code, open a yamux stream to it, and splice
// ciphertext both ways. The relay never decrypts — the Noise handshake + frames
// run end-to-end between joiner and host through this pipe.
func handleJoiner(c net.Conn, ctrl []byte, reg *registry) {
	var jc wormhole.JoinCtrl
	if json.Unmarshal(ctrl, &jc) != nil || jc.Code == "" {
		_ = wormhole.WriteAck(c, false)
		c.Close()
		return
	}
	sess := reg.get(jc.Code)
	if sess == nil {
		_ = wormhole.WriteAck(c, false) // no live line for that code
		c.Close()
		return
	}
	stream, err := sess.OpenStream()
	if err != nil {
		_ = wormhole.WriteAck(c, false)
		c.Close()
		return
	}
	if wormhole.WriteAck(c, true) != nil {
		stream.Close()
		c.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(stream, c); done <- struct{}{} }() // joiner → host (ciphertext)
	go func() { io.Copy(c, stream); done <- struct{}{} }() // host → joiner (ciphertext)
	<-done
	stream.Close()
	c.Close()
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
