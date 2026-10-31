package relayserver

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
	"partyline.sh/partyline/internal/gatewayjoin"
)

type JoinRequest struct {
	ID      string `json:"id"`
	Machine string `json:"machine"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Expires int64  `json:"expires"`
}
type pendingJoin struct {
	JoinRequest
	result chan gatewayjoin.Result
}

func (s *Server) initJoining(path string) error {
	var err error
	s.joinKey, err = gatewayjoin.LoadKey(filepath.Join(filepath.Dir(path), "gateway-key"))
	s.joins = map[string]*pendingJoin{}
	s.joinSockets = map[*websocket.Conn]struct{}{}
	return err
}

func (s *Server) joinConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && o != s.origin {
		refuse(w)
		return
	}
	s.mu.Lock()
	if s.closed || s.joining >= 16 {
		s.mu.Unlock()
		http.Error(w, "join limit", 429)
		return
	}
	s.joining++
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.joining--; s.mu.Unlock(); s.wg.Done() }()
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.joinSockets[c] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.joinSockets, c); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	defer cancel()
	handshake, done := context.WithTimeout(ctx, 10*time.Second)
	channel, err := gatewayjoin.Accept(handshake, c, s.joinKey)
	done()
	if err != nil {
		return
	}
	entry := &pendingJoin{JoinRequest: JoinRequest{ID: channel.ID, Machine: channel.Request.Machine, Name: channel.Request.Name, Code: channel.Code, Expires: s.now().Add(5 * time.Minute).UnixMilli()}, result: make(chan gatewayjoin.Result, 1)}
	s.mu.Lock()
	s.joins[entry.ID] = entry
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.joins, entry.ID); s.mu.Unlock() }()
	closed := make(chan error, 1)
	go func() { closed <- channel.WaitClosed(ctx) }()
	select {
	case <-ctx.Done():
		return
	case <-closed:
		return
	case result := <-entry.result:
		write, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		_ = channel.Send(write, result)
	}
}
func (s *Server) pendingJoins() []JoinRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []JoinRequest{}
	for _, p := range s.joins {
		if p.Expires > s.now().UnixMilli() {
			out = append(out, p.JoinRequest)
		}
	}
	return out
}
func (s *Server) approveJoin(id, code string, approved bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.joins[id]
	if p == nil || p.Expires <= s.now().UnixMilli() || p.Code != code {
		return false
	}
	result := gatewayjoin.Result{Approved: approved, Reason: "Join declined on the private gateway"}
	if approved {
		token := randomSecret()
		if _, err := s.db.Exec("INSERT INTO machines(id,hash) VALUES(?,?)", p.Machine, Digest(token)); err != nil {
			return false
		}
		result = gatewayjoin.Result{Approved: true, Token: token}
	}
	delete(s.joins, id)
	p.result <- result
	return true
}
