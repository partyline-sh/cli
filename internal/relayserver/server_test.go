package relayserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"partyline.sh/partyline/internal/relaycontract"
)

var fixtureID = strings.Repeat("11", 16)
var fixtureToken = strings.Repeat("22", 32)

func fixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := Open(Config{Database: filepath.Join(t.TempDir(), "private", "registry.sqlite"), Origin: "https://relay.test", InitialID: fixtureID, InitialHash: Digest(fixtureToken)})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewUnstartedServer(s)
	h.Config.ReadTimeout = 15 * time.Second
	h.Config.WriteTimeout = 15 * time.Second
	h.Start()
	t.Cleanup(func() { s.Close(); h.Close() })
	return s, h
}
func TestHostNeutralContract(t *testing.T) {
	_, h := fixture(t)
	relaycontract.Run(t, relaycontract.Fixture{Origin: h.URL, Machine: fixtureID, Token: fixtureToken})
}
func TestPersistenceAndBootstrapCannotRotateIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "registry.sqlite")
	c := Config{Database: path, Origin: "https://relay.test", InitialID: fixtureID, InitialHash: Digest(fixtureToken)}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec("INSERT INTO invites(hash,expires) VALUES(?,?)", Digest(strings.Repeat("a", 64)), time.Now().Add(time.Minute).UnixMilli())
	s.Close()
	c.InitialHash = Digest(strings.Repeat("b", 64))
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var journal string
	var synchronous int
	if err = s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatal("WAL not active", err)
	}
	if err = s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatal("durable synchronous FULL not active", err)
	}
	if !s.authenticated(fixtureID, fixtureToken) {
		t.Fatal("restart replaced bootstrap identity")
	}
	if s.authenticated(fixtureID, strings.Repeat("b", 64)) {
		t.Fatal("replacement hash accepted")
	}
	if _, err = s.join(strings.Repeat("33", 16), strings.Repeat("a", 64)); err != nil {
		t.Fatal("invite lost on restart:", err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal("registry not private")
	}
}
func TestExpiredInvitesAndOneUseDeviceRoute(t *testing.T) {
	s, h := fixture(t)
	route := strings.Repeat("44", 16)
	token := strings.Repeat("55", 32)
	for _, expires := range []int64{time.Now().Add(-time.Second).UnixMilli(), time.Now().Add(time.Minute).UnixMilli()} {
		s.db.Exec("INSERT INTO device_invites(hash,machine,route,expires) VALUES(?,?,?,?)", Digest(token), fixtureID, route, expires)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		p, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"role": "invite", "token": token, "min": 1, "max": 1})
		p.Write(ctx, websocket.MessageBinary, frame(1, route, json.RawMessage(b)))
		_, _, err = p.Read(ctx)
		if expires < time.Now().UnixMilli() && err == nil {
			t.Fatal("expired invite accepted")
		}
		if expires > time.Now().UnixMilli() && err != nil {
			t.Fatal("valid invite refused", err)
		}
		p.CloseNow()
		cancel()
		s.db.Exec("DELETE FROM device_invites")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM device_invites").Scan(&count)
	if count != 0 {
		t.Fatal("device invite not consumed")
	}
}
func TestBrowserOriginAndBoundedBodies(t *testing.T) {
	_, h := fixture(t)
	for _, test := range []struct {
		origin, body string
		status       int
	}{{"https://evil.test", "{}", 403}, {"", strings.Repeat("x", 4097), 413}} {
		r, _ := http.NewRequest("POST", h.URL+"/status", strings.NewReader(test.body))
		r.Header.Set("Origin", test.origin)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != test.status {
			t.Fatal("wrong refusal", res.StatusCode)
		}
	}
	r, _ := http.NewRequest("GET", h.URL+"/connect", nil)
	r.Header.Set("Origin", "https://evil.test")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("foreign origin accepted")
	}
}
func TestPrivateDatabaseRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte{}, 0600)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	if db, err := openStore(link, "", ""); err == nil {
		db.Close()
		t.Fatal("followed database symlink")
	}
}

func TestSlowPeerIsRemovedWithoutBlockingAnotherConnection(t *testing.T) {
	s, h := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone := strings.Repeat("66", 16)
	token := strings.Repeat("77", 32)
	if _, err := s.db.Exec("INSERT INTO devices(machine,phone,hash) VALUES(?,?,?)", fixtureID, phone, Digest(token)); err != nil {
		t.Fatal(err)
	}
	p, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseNow()
	if err = p.Write(ctx, websocket.MessageBinary, frame(1, phone, map[string]any{"role": "phone", "machine": fixtureID, "token": token, "min": 1, "max": 1})); err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.Read(ctx); err != nil {
		t.Fatal(err)
	}
	healthy, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.CloseNow()
	s.mu.Lock()
	var slow *peer
	for peer := range s.peers {
		if peer.id == phone {
			slow = peer
			break
		}
	}
	if slow == nil {
		s.mu.Unlock()
		t.Fatal("missing phone peer")
	}
	data := bytes.Repeat([]byte{0x42}, MaxFrame)
	for i := 0; i < 1000; i++ {
		if _, ok := s.peers[slow]; !ok {
			break
		}
		s.binaryLocked(slow, data)
	}
	_, stillPresent := s.peers[slow]
	s.mu.Unlock()
	if stillPresent {
		t.Fatal("slow peer kept an unbounded backlog")
	}
	if err = healthy.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	kind, b, err := healthy.Read(ctx)
	if err != nil || kind != websocket.MessageText || string(b) != "pong" {
		t.Fatal("slow peer blocked independent connection", err)
	}
}

func TestTrustedProxyBucketsUseOnlyLastHopAndIPv6Prefix(t *testing.T) {
	s, _ := fixture(t)
	request := func(peer, forwarded string) *http.Request {
		r := httptest.NewRequest("GET", "https://relay.test/connect", nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-For", forwarded)
		return r
	}
	if key := s.rateKey(request("127.0.0.1:3000", "203.0.113.7, 198.51.100.8")); key != "198.51.100.8" {
		t.Fatal("trusted proxy did not use last hop", key)
	}
	if key := s.rateKey(request("192.0.2.1:3000", "198.51.100.8")); key != "192.0.2.1" {
		t.Fatal("untrusted peer spoofed forwarded client", key)
	}
	a := s.rateKey(request("[2001:db8:1:2::1]:3000", ""))
	b := s.rateKey(request("[2001:db8:1:2::abcd]:3000", ""))
	if a != b {
		t.Fatal("one IPv6 /64 gets separate buckets")
	}
	for i := 0; i < 120; i++ {
		if !s.allowHTTP(request("127.0.0.1:3000", "198.51.100.1")) {
			t.Fatal("limit applied early")
		}
	}
	if s.allowHTTP(request("127.0.0.1:3000", "198.51.100.1")) {
		t.Fatal("attacker bucket not limited")
	}
	if !s.allowHTTP(request("127.0.0.1:3000", "198.51.100.2")) {
		t.Fatal("one proxy client locked out another")
	}
	s.mu.Lock()
	s.requests = map[string]requestLimit{}
	for i := 0; i < 4096; i++ {
		s.requests[fmt.Sprint(i)] = requestLimit{at: time.Now().Add(-time.Minute)}
	}
	s.mu.Unlock()
	if !s.allowHTTP(request("203.0.113.9:3000", "")) {
		t.Fatal("full table locked out a new client")
	}
}

func TestUnauthenticatedConnectionsHaveSeparateCap(t *testing.T) {
	s, h := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connections := []*websocket.Conn{}
	defer func() {
		for _, c := range connections {
			c.CloseNow()
		}
	}()
	for i := 0; i < 32; i++ {
		c, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, c)
	}
	if c, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil); err == nil {
		c.CloseNow()
		t.Fatal("unauthenticated cap not enforced")
	}
	s.mu.Lock()
	count := len(s.peers)
	s.mu.Unlock()
	if count > 32 {
		t.Fatal("unauthenticated sockets consumed authenticated capacity")
	}
}

func TestSendingPhoneCannotEvictComputerOnFullQueue(t *testing.T) {
	s, h := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.CloseNow()
	if err = m.Write(ctx, websocket.MessageBinary, frame(1, fixtureID, map[string]any{"role": "machine", "token": fixtureToken, "min": 1, "max": 1})); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.Read(ctx); err != nil {
		t.Fatal(err)
	}
	phone := strings.Repeat("88", 16)
	token := strings.Repeat("99", 32)
	s.db.Exec("INSERT INTO devices(machine,phone,hash) VALUES(?,?,?)", fixtureID, phone, Digest(token))
	p, _, err := websocket.Dial(ctx, strings.Replace(h.URL, "http", "ws", 1)+"/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseNow()
	p.Write(ctx, websocket.MessageBinary, frame(1, phone, map[string]any{"role": "phone", "machine": fixtureID, "token": token, "min": 1, "max": 1}))
	if _, _, err = p.Read(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	var computer, sender *peer
	for peer := range s.peers {
		if peer.id == fixtureID {
			computer = peer
		}
		if peer.id == phone {
			sender = peer
		}
	}
	if computer == nil || sender == nil {
		s.mu.Unlock()
		t.Fatal("missing peers")
	}
	computer.queuedBytes = 2 << 20 // Inject the queue pressure independently of OS socket buffers.
	s.forwardLocked(sender, computer, frame(2, phone, strings.Repeat("x", 32)))
	_, computerPresent := s.peers[computer]
	_, phonePresent := s.peers[sender]
	s.mu.Unlock()
	if !computerPresent || phonePresent {
		t.Fatal("full shared queue evicted the wrong connection")
	}
}
