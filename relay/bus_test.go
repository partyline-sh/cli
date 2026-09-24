package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"partyline.sh/partyline/internal/bus"
)

// The bus fans messages out across an org. If verifyBus ever said yes when the control plane did
// not, one team would receive another team's notifications. Every case below is a way that could
// happen, so each one must fail closed.
func TestVerifyBusFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rejected token", 401, `{}`},
		{"control plane erroring", 500, `oops`},
		{"unparseable answer", 200, `<html>a proxy error page</html>`},
		{"no org in answer", 200, `{"identity":"u1"}`},
		{"no identity in answer", 200, `{"org":"acme"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			t.Setenv("RELAY_API", srv.URL)
			if _, ok := verifyBus("some-token", "laptop"); ok {
				t.Fatalf("verifyBus accepted a token the control plane did not confirm (%s)", tc.name)
			}
		})
	}
}

// An empty token must not even reach the control plane: a blank Authorization header is the
// commonest way a misconfigured client arrives, and it should cost nothing to refuse.
func TestVerifyBusRefusesEmptyTokenWithoutCallingOut(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(identity{Org: "acme", Identity: "u1"})
	}))
	defer srv.Close()
	t.Setenv("RELAY_API", srv.URL)
	if _, ok := verifyBus("   ", "laptop"); ok {
		t.Fatal("verifyBus accepted a blank token")
	}
	if called {
		t.Fatal("verifyBus called the control plane for a blank token")
	}
}

// The happy path, and the shape the rest of the bus depends on: identity comes from the control
// plane's answer, not from anything the caller sent.
func TestVerifyBusReturnsControlPlaneIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("token not forwarded: %q", got)
		}
		_ = json.NewEncoder(w).Encode(identity{Org: "acme", Identity: "darcy:laptop"})
	}))
	defer srv.Close()
	t.Setenv("RELAY_API", srv.URL)
	who, ok := verifyBus("tok-123", "laptop")
	if !ok || who.Org != "acme" || who.Identity != "darcy:laptop" {
		t.Fatalf("verifyBus = %+v, %v; want acme/darcy:laptop, true", who, ok)
	}
}

func TestSplitListDropsBlanks(t *testing.T) {
	got := splitList(" a , ,b,, c ")
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("splitList = %v, want %v", got, want)
	}
}

// End to end over a real socket: two machines in one org, subscribed to one project, and a fact
// recorded on one arriving at the other. The hub's unit tests cover fan-out in memory; this
// covers the parts only a live connection exercises — upgrade, auth, scoping and JSON framing.
func TestTwoMachinesExchangeNewsOverARealSocket(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Identity comes from the token, as the control plane would derive it from the session.
		_ = json.NewEncoder(w).Encode(identity{Org: "acme", Identity: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	}))
	defer cp.Close()
	t.Setenv("RELAY_API", cp.URL)

	hub := bus.NewHub()
	srv := httptest.NewServer(busHandler(hub))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/bus?projects=acr"

	dial := func(who string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + who}},
		})
		if err != nil {
			t.Fatalf("dial %s: %v", who, err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}

	matt := dial("matt:laptop")
	darcy := dial("darcy:laptop")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := wsjson.Write(ctx, darcy, bus.Envelope{Type: bus.TypeMemoryChanged, Project: "acr",
		Payload: json.RawMessage(`{"project":"acr"}`)}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Presence announcements arrive first; read past them to the news.
	for {
		var got bus.Envelope
		if err := wsjson.Read(ctx, matt, &got); err != nil {
			t.Fatalf("matt never received the change: %v", err)
		}
		if got.Type != bus.TypeMemoryChanged {
			continue
		}
		if got.From != "darcy:laptop" {
			t.Fatalf("From = %q; the server must stamp the sender, not the client", got.From)
		}
		if got.Project != "acr" {
			t.Fatalf("Project = %q, want acr", got.Project)
		}
		return
	}
}

// A connection may only publish into a project it subscribed to. Without this, one token could
// inject news into every project in its org.
func TestAConnectionCannotPublishIntoAProjectItDidNotJoin(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Distinct identities: the hub keys connections by identity, so reusing one here would
		// evict the listener instead of testing the scoping rule.
		_ = json.NewEncoder(w).Encode(identity{Org: "acme", Identity: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	}))
	defer cp.Close()
	t.Setenv("RELAY_API", cp.URL)

	hub := bus.NewHub()
	srv := httptest.NewServer(busHandler(hub))
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/bus?projects="

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	listener, _, err := websocket.Dial(ctx, base+"secret", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer t"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.CloseNow()

	sender, _, err := websocket.Dial(ctx, base+"other", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer t2"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.CloseNow()

	if err := wsjson.Write(ctx, sender, bus.Envelope{Type: bus.TypeMemoryChanged, Project: "secret",
		Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}

	short, scancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer scancel()
	for {
		var got bus.Envelope
		if err := wsjson.Read(short, listener, &got); err != nil {
			return // timed out without the message: correct
		}
		if got.Type == bus.TypeMemoryChanged {
			t.Fatal("a connection published into a project it never subscribed to")
		}
	}
}

// One person, two machines — the case the whole bus exists for (a laptop and a server both in
// the same project). The hub keys connections by identity and REPLACES on reconnect, so if both
// machines arrive with the same identity the second one silently evicts the first: the laptop
// stops receiving news the moment the server connects, and nothing reports an error.
//
// The machine name the client sends must therefore reach the control plane, which is what
// distinguishes the two identities.
func TestTwoMachinesOfOneUserAreDistinctParticipants(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Machine string `json:"machine"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// What the real route does: identity is anchored to the user id, decorated by the machine.
		id := "user-1"
		if body.Machine != "" {
			id += ":" + body.Machine
		}
		_ = json.NewEncoder(w).Encode(identity{Org: "acme", Identity: id})
	}))
	defer cp.Close()
	t.Setenv("RELAY_API", cp.URL)

	hub := bus.NewHub()
	srv := httptest.NewServer(busHandler(hub))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/bus?projects=acr"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dial := func(machine string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer t"}, "X-Machine": {machine}}})
		if err != nil {
			t.Fatalf("dial %s: %v", machine, err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	dial("laptop")
	dial("monolith")

	// Both must still be on the bus. One connection count means the second evicted the first.
	deadline := time.Now().Add(3 * time.Second)
	for hub.Count() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := hub.Count(); n != 2 {
		t.Fatalf("hub has %d connection(s); a user's two machines collapsed into one identity, so one of them silently stopped receiving", n)
	}
}

// A joiner must learn who is ALREADY connected. Without a roster it finds out only when an
// existing peer next says something, so a bar would read "nobody here" while two teammates sat
// connected to the same project.
func TestAJoinerIsHandedTheRosterAndItsOwnIdentity(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Machine string `json:"machine"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(identity{Org: "acme", Identity: "user-1:" + body.Machine})
	}))
	defer cp.Close()
	t.Setenv("RELAY_API", cp.URL)

	hub := bus.NewHub()
	srv := httptest.NewServer(busHandler(hub))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/bus?projects=acr"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := func(machine string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer t"}, "X-Machine": {machine}}})
		if err != nil {
			t.Fatalf("dial %s: %v", machine, err)
		}
		return c
	}

	first := dial("laptop")
	defer first.CloseNow()
	// Drain the first machine's own roster so the next read is about the second machine.
	var ignored bus.Envelope
	_ = wsjson.Read(ctx, first, &ignored)

	second := dial("monolith")

	// The second machine's roster must name the first, and tell it who it is.
	for {
		var got bus.Envelope
		if err := wsjson.Read(ctx, second, &got); err != nil {
			t.Fatalf("no roster: %v", err)
		}
		var body struct {
			Peers []string `json:"peers"`
			You   string   `json:"you"`
		}
		if json.Unmarshal(got.Payload, &body) != nil || body.You == "" {
			continue
		}
		if body.You != "user-1:monolith" {
			t.Fatalf("you = %q, want user-1:monolith", body.You)
		}
		var sawFirst bool
		for _, p := range body.Peers {
			if p == "user-1:laptop" {
				sawFirst = true
			}
		}
		if !sawFirst {
			t.Fatalf("roster %v does not include the machine already connected", body.Peers)
		}
		break
	}

	// And when it leaves, the first machine is told — rather than showing it as present until
	// the entry ages out a minute and a half later.
	second.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var got bus.Envelope
		if err := wsjson.Read(ctx, first, &got); err != nil {
			t.Fatalf("no departure: %v", err)
		}
		var body struct {
			Left string `json:"left"`
		}
		_ = json.Unmarshal(got.Payload, &body)
		if body.Left == "user-1:monolith" {
			return
		}
	}
	t.Fatal("a disconnect was never announced, so peers would show a departed machine as present")
}
