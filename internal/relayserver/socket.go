package relayserver

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const MaxFrame = 65552

type packet struct {
	kind websocket.MessageType
	data []byte
}
type peer struct {
	conn              *websocket.Conn
	id, role, machine string
	expires           time.Time
	deadline          time.Time
	ctx               context.Context
	cancel            context.CancelFunc
	queue             chan packet
	control           chan packet
	queuedBytes       int
	last              time.Time
	bytes, messages   float64
}

func frame(kind byte, id string, body any) []byte {
	route, _ := hex.DecodeString(id)
	b, _ := json.Marshal(body)
	return append(append([]byte{kind}, route...), b...)
}
func (p *peer) stop() { p.cancel(); p.conn.CloseNow() }

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.mu.Lock()
	full := s.closed || len(s.peers) >= 256 || s.unauthenticatedLocked() >= 32
	s.mu.Unlock()
	if full {
		http.Error(w, "connection limit", 503)
		return
	}
	u, _ := url.Parse(s.origin)
	// Browser clients must come from the configured origin. Native clients have
	// no Origin header; they still authenticate the relay and its route separately.
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
		refuse(w)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{u.Host}})
	if err != nil {
		return
	}
	c.SetReadLimit(MaxFrame)
	ctx, cancel := context.WithCancel(s.ctx)
	p := &peer{conn: c, ctx: ctx, cancel: cancel, queue: make(chan packet, 512), control: make(chan packet, 256), deadline: s.now().Add(10 * time.Second), last: s.now(), bytes: 40 << 20, messages: 240}
	s.mu.Lock()
	if s.closed || len(s.peers) >= 256 || s.unauthenticatedLocked() >= 32 {
		s.mu.Unlock()
		p.stop()
		return
	}
	s.peers[p] = struct{}{}
	s.wg.Add(2)
	s.mu.Unlock()
	go s.writeLoop(p)
	defer s.wg.Done()
	defer func() { s.mu.Lock(); s.removeLocked(p); s.mu.Unlock() }()
	for {
		s.mu.Lock()
		deadline := s.now().Add(75 * time.Second)
		if p.id == "" {
			deadline = p.deadline
		}
		if !p.expires.IsZero() && p.expires.Before(deadline) {
			deadline = p.expires
		}
		s.mu.Unlock()
		readCtx, done := context.WithDeadline(ctx, deadline)
		kind, data, err := c.Read(readCtx)
		done()
		if err != nil {
			return
		}
		s.mu.Lock()
		if _, ok := s.peers[p]; !ok {
			s.mu.Unlock()
			return
		}
		elapsed := s.now().Sub(p.last).Seconds()
		p.last = s.now()
		p.bytes = min(float64(40<<20), p.bytes+elapsed*float64(4<<20))
		p.messages = min(240, p.messages+elapsed*120)
		valid := p.bytes >= float64(len(data)) && p.messages >= 1
		p.bytes -= float64(len(data))
		p.messages--
		if valid && kind == websocket.MessageText && string(data) == "ping" {
			s.deliverLocked(p, packet{websocket.MessageText, []byte("pong")})
			s.mu.Unlock()
			continue
		}
		if !valid || kind != websocket.MessageBinary || len(data) < 17 || len(data) > MaxFrame || (!p.expires.IsZero() && !s.now().Before(p.expires)) {
			s.removeLocked(p)
			s.mu.Unlock()
			return
		}
		if p.id == "" {
			valid = s.helloLocked(p, data)
		} else {
			valid = s.routeLocked(p, data)
		}
		if !valid {
			s.removeLocked(p)
		}
		s.mu.Unlock()
		if !valid {
			return
		}
	}
}

func (s *Server) writeLoop(p *peer) {
	defer s.wg.Done()
	defer p.conn.CloseNow()
	for {
		var item packet
		select {
		case item = <-p.control:
		default:
			select {
			case <-p.ctx.Done():
				return
			case item = <-p.control:
			case item = <-p.queue:
				s.mu.Lock()
				p.queuedBytes -= len(item.data)
				s.mu.Unlock()
			}
		}
		ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
		err := p.conn.Write(ctx, item.kind, item.data)
		cancel()
		if err != nil {
			s.mu.Lock()
			s.removeLocked(p)
			s.mu.Unlock()
			return
		}
	}
}

func (s *Server) unauthenticatedLocked() int {
	n := 0
	for p := range s.peers {
		if p.id == "" {
			n++
		}
	}
	return n
}

// Called only under s.mu. Enqueue without waiting on a slow reader or its network.
func (s *Server) deliverLocked(p *peer, item packet) {
	if _, ok := s.peers[p]; !ok {
		return
	}
	select {
	case p.control <- item:
	default:
		// Do not let a flood of notifications evict a healthy computer. Its
		// receiver expires phone leases even if a gone notice cannot be queued.
		if p.role != "machine" {
			s.removeLocked(p)
		}
	}
}

func (s *Server) forwardLocked(source, target *peer, data []byte) {
	if _, ok := s.peers[target]; !ok {
		return
	}
	accepted := false
	if target.queuedBytes+len(data) <= 2<<20 {
		select {
		case target.queue <- packet{websocket.MessageBinary, data}:
			target.queuedBytes += len(data)
			accepted = true
		default:
		}
	}
	if !accepted {
		// Computers are shared by many phones. A sender exceeding a slow
		// computer's bounded queue loses only its own route, never the computer.
		if target.role == "machine" {
			s.removeLocked(source)
		} else {
			s.removeLocked(target)
		}
	}
}
func (s *Server) binaryLocked(p *peer, b []byte) {
	s.deliverLocked(p, packet{websocket.MessageBinary, b})
}
func (s *Server) removeLocked(p *peer) {
	if _, ok := s.peers[p]; !ok {
		return
	}
	delete(s.peers, p)
	p.stop()
	for other := range s.peers {
		if p.role == "phone" && other.role == "machine" && other.id == p.machine {
			s.binaryLocked(other, frame(4, p.id, map[string]any{}))
		}
		if p.role == "machine" && other.role == "phone" && other.machine == p.id {
			s.binaryLocked(other, frame(4, other.id, map[string]any{}))
		}
	}
}

func (s *Server) helloLocked(p *peer, data []byte) bool {
	// Registration checks are serialized with revocation. A stalled registry
	// can temporarily delay routing (SQLite busy_timeout is bounded at 5s).
	if data[0] != 1 || len(data) > 4096 || !utf8.Valid(data[17:]) || !s.now().Before(p.deadline) {
		return false
	}
	id := hex.EncodeToString(data[1:17])
	var b struct {
		Role, Token, Machine string
		Min, Max             int
	}
	if json.Unmarshal(data[17:], &b) != nil || b.Min != 1 || b.Max != 1 || !secretPattern.MatchString(b.Token) {
		return false
	}
	switch b.Role {
	case "machine":
		if !s.authenticated(id, b.Token) {
			return false
		}
	case "phone":
		if !idPattern.MatchString(b.Machine) {
			return false
		}
		var found string
		if s.db.QueryRow("SELECT phone FROM devices WHERE machine=? AND phone=? AND hash=?", b.Machine, id, Digest(b.Token)).Scan(&found) != nil {
			return false
		}
	case "invite":
		var expires int64
		if s.db.QueryRow("DELETE FROM device_invites WHERE hash=? AND route=? AND expires>? RETURNING machine,expires", Digest(b.Token), id, s.now().UnixMilli()).Scan(&b.Machine, &expires) != nil {
			return false
		}
		p.expires = time.UnixMilli(expires)
	default:
		return false
	}
	for old := range s.peers {
		if old != p && old.id == id && old.role == b.Role && (b.Role != "phone" || old.machine == b.Machine) {
			s.removeLocked(old)
		}
	}
	p.id, p.role, p.machine = id, b.Role, b.Machine
	s.binaryLocked(p, frame(1, id, map[string]any{"ok": true, "version": 1}))
	return true
}

func (s *Server) routeLocked(p *peer, data []byte) bool {
	if data[0] != 2 {
		return false
	}
	id := hex.EncodeToString(data[1:17])
	if p.role == "phone" {
		if id != p.id {
			return false
		}
		for target := range s.peers {
			if target.role == "machine" && target.id == p.machine {
				s.forwardLocked(p, target, data)
				return true
			}
		}
		s.binaryLocked(p, frame(4, p.id, map[string]any{}))
		return true
	}
	if p.role == "machine" && id == p.id {
		type entry struct {
			id   string
			data []byte
		}
		entries := []entry{}
		for at := 17; at < len(data); {
			if at+20 > len(data) {
				return false
			}
			route := hex.EncodeToString(data[at : at+16])
			length := int(binary.BigEndian.Uint32(data[at+16 : at+20]))
			at += 20
			if length < 16 || length > 65535 || length > len(data)-at {
				return false
			}
			entries = append(entries, entry{route, data[at : at+length]})
			at += length
		}
		// Parse the entire batch before forwarding even the first entry.
		for _, e := range entries {
			for target := range s.peers {
				if target.role == "phone" && target.machine == p.id && target.id == e.id {
					route, _ := hex.DecodeString(e.id)
					s.forwardLocked(p, target, append(append([]byte{2}, route...), e.data...))
					break
				}
			}
		}
		return true
	}
	if p.role == "invite" {
		if id != p.id {
			return false
		}
		for target := range s.peers {
			if target.role == "machine" && target.id == p.machine {
				s.forwardLocked(p, target, data)
				return true
			}
		}
		return false
	}
	if p.role == "machine" {
		for target := range s.peers {
			if target.role == "invite" && target.machine == p.id && target.id == id && s.now().Before(target.expires) {
				s.forwardLocked(p, target, data)
				return true
			}
		}
		return true
	}
	return false
}
