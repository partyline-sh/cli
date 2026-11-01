package relayserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"partyline.sh/partyline/internal/gatewayjoin"
)

func beginJoin(t *testing.T, s *Server, origin string) (*gatewayjoin.Channel, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, strings.Replace(origin, "http", "ws", 1)+"/join-connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	channel, err := gatewayjoin.Begin(ctx, c, gatewayjoin.Request{Machine: strings.Repeat("aa", 16), Name: "Workstation\x1b\n"})
	if err != nil {
		t.Fatal(err)
	}
	for len(s.pendingJoins()) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("no pending join")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	return channel, c
}

func TestJoiningRequiresGatewayApprovalBoundToNoiseCode(t *testing.T) {
	s, h := fixture(t)
	channel, _ := beginJoin(t, s, h.URL)
	pending := s.pendingJoins()
	if len(pending) != 1 || pending[0].Code != channel.Code || pending[0].ID != channel.ID || len(channel.Code) != 6 {
		t.Fatal("join code not bound on both sides")
	}
	if pending[0].Name != "Workstation" {
		t.Fatal("untrusted terminal controls in name")
	}
	if s.authenticated(strings.Repeat("aa", 16), fixtureToken) {
		t.Fatal("machine authorized before approval")
	}
	if s.approveJoin(channel.ID, "wrong", true) {
		t.Fatal("wrong verification code approved")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !s.approveJoin(channel.ID, channel.Code, true) {
		t.Fatal("verified join refused")
	}
	result, err := channel.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Approved || len(result.Token) != 64 || !s.authenticated(strings.Repeat("aa", 16), result.Token) {
		t.Fatal("approved credential not delivered")
	}
	if s.approveJoin(channel.ID, channel.Code, true) {
		t.Fatal("approval replay accepted")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM devices").Scan(&count)
	if count != 0 {
		t.Fatal("gateway join authorized a phone")
	}
}
func TestDeclineAndDisconnectDoNotAuthorizeMachines(t *testing.T) {
	s, h := fixture(t)
	channel, _ := beginJoin(t, s, h.URL)
	if !s.approveJoin(channel.ID, channel.Code, false) {
		t.Fatal("decline failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := channel.Receive(ctx)
	if err != nil || result.Approved || result.Token != "" {
		t.Fatal("decline delivered credentials", err)
	}
	channel, c := beginJoin(t, s, h.URL)
	c.CloseNow()
	for len(s.pendingJoins()) != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("cancelled request retained")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if s.approveJoin(channel.ID, channel.Code, true) {
		t.Fatal("cancelled request approved")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM machines WHERE id=?", strings.Repeat("aa", 16)).Scan(&count)
	if count != 0 {
		t.Fatal("unauthorized registration")
	}
}
func TestJoinRequestsAreVisibleOnlyToGatewayOwner(t *testing.T) {
	s, h := fixture(t)
	_, _ = beginJoin(t, s, h.URL)
	id := strings.Repeat("bb", 16)
	token := strings.Repeat("cc", 32)
	s.db.Exec("INSERT INTO machines(id,hash) VALUES(?,?)", id, Digest(token))
	for _, auth := range []struct {
		id, token string
		status    int
	}{{id, token, 403}, {fixtureID, fixtureToken, 200}} {
		b, _ := json.Marshal(map[string]string{"id": auth.id, "token": auth.token})
		r, err := http.Post(h.URL+"/join-requests", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != auth.status {
			t.Fatal("join approval exposed to member", r.StatusCode)
		}
	}
}
