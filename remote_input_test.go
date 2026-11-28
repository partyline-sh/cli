package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

func remoteInputFixture(t *testing.T) (*remoteInputStore, string) {
	t.Helper()
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	route := "11223344556677889900112233445566"
	b, _ := json.Marshal([]remotePhone{{ID: route}})
	if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return &remoteInputStore{phones: map[string]*remoteInputHistory{}}, route
}

// A real raw-terminal program records the exact input stream and optionally opts
// into bracketed paste. No shell or live partyline tmux server is involved.
func remoteInputRecorder(t *testing.T, bracketed bool) (string, func() []byte) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	path := filepath.Join(t.TempDir(), "bytes")
	script := `import os,sys,tty
 tty.setraw(0)
 if sys.argv[2] == "true": os.write(1,b"\x1b[?2004h")
 open(sys.argv[1]+".ready","w").close()
 while True:
  b=os.read(0,65536)
  with open(sys.argv[1],"ab",buffering=0) as f: f.write(b)
 `
	// Strip the leading space shared by Python's top-level lines.
	script = strings.ReplaceAll(script, "\n ", "\n")
	session := tmuxSessionName
	if !bracketed {
		session = contractorSession
	}
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session, "--", python, "-u", "-c", script, path, fmt.Sprint(bracketed)).CombinedOutput()
	if err != nil {
		t.Fatalf("recorder: %v %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recorder not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return strings.TrimSpace(string(out)), func() []byte { b, _ := os.ReadFile(path); return b }
}
func waitRemoteBytes(t *testing.T, read func() []byte, want []byte) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := read()
		if bytes.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bytes: got %q want %q", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestRemoteInput(t *testing.T) {
	tmuxTestServer(t, "ptln-test-remote-input")
	store, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	now := time.Now()
	sequence := 0
	send := func(kind, data string) remote.Message {
		sequence++
		return store.handle(route, remote.Message{Type: "input", ID: fmt.Sprint(sequence), Pane: pane, Kind: kind, Data: data}, now.Add(time.Duration(sequence)*time.Second))
	}
	var want []byte
	check := func(kind, data, expected string) {
		t.Helper()
		r := send(kind, data)
		if r.Status != "ok" {
			t.Fatalf("%s: %+v", kind, r)
		}
		want = append(want, expected...)
		waitRemoteBytes(t, read, want)
	}
	check("text", "-literal", "-literal\r")
	check("text", ";", ";\r")
	check("text", "literal;", "literal;\r")
	check("text", "echo hi; echo bye", "echo hi; echo bye\r")
	check("text", "\\;", "\\;\r")
	check("paste", "first\nsecond", "\x1b[200~first\rsecond\x1b[201~")
	check("text", "multi\nline", "\x1b[200~multi\rline\x1b[201~\r")
	keyBytes := []string{"\x1b", "\r", "\t", "\x1b[Z", "\x7f", "\x1b[A", "\x1b[B", "\x1b[D", "\x1b[C", "\x1b[5~", "\x1b[6~", "\x03", "\x04", "\x12", "\x0c"}
	for i, key := range remoteInputKeys {
		check("key", key, keyBytes[i])
	}
	original := remote.Message{Type: "input", ID: "duplicate", Pane: pane, Kind: "paste", Data: "once"}
	r := store.handle(route, original, now.Add(time.Minute))
	if r.Status != "ok" {
		t.Fatal(r)
	}
	want = append(want, "\x1b[200~once\x1b[201~"...)
	waitRemoteBytes(t, read, want)
	// Simulate a new machine transport as well as a new phone peer.
	channel := newRemoteChannel()
	channel.inputs = store
	if got := channel.inputs.handle(route, original, now.Add(2*time.Minute)); got != r {
		t.Fatalf("duplicate result: %+v", got)
	}
	time.Sleep(40 * time.Millisecond)
	waitRemoteBytes(t, read, want)
	outside, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "unrelated", "--", "sleep", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("outside pane: %v %s", err, outside)
	}
	bad := []remote.Message{
		{Type: "input", Pane: strings.TrimSpace(string(outside)), Kind: "text", Data: "not authorized"},
		{Type: "input", Pane: "%999999", Kind: "text", Data: "bad"},
		{Type: "input", Pane: pane, Kind: "key", Data: "F1"},
		{Type: "input", Pane: pane, Kind: "text", Data: strings.Repeat("x", 24*1024+1)},
		{Type: "input", Pane: pane, Kind: "paste", Data: "\x1b"},
		{Type: "input", Pane: pane, Kind: "text", Data: strings.Repeat("<", 24*1024)},
	}
	for i, m := range bad {
		m.ID = fmt.Sprintf("bad%d", i)
		got := store.handle(route, m, now.Add(3*time.Minute))
		if got.Status != "refused" || got.Reason == "" {
			t.Fatal(got)
		}
	}
	waitRemoteBytes(t, read, want)
	event, err := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(event, []byte("literal")) || bytes.Contains(event, []byte("first")) {
		t.Fatal("audit leaks input")
	}
	var entries []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(event), []byte("\n")) {
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 23 || entries[0]["size"] != float64(len("-literal")) || entries[0]["phoneId"] != route || entries[0]["pane"] != pane {
		t.Fatalf("audit: %s", event)
	}
	plainPane, plainRead := remoteInputRecorder(t, false)
	m := remote.Message{Type: "input", ID: "plain", Pane: plainPane, Kind: "paste", Data: "one\ntwo"}
	if r := store.handle(route, m, now.Add(4*time.Minute)); r.Status != "ok" {
		t.Fatal(r)
	}
	waitRemoteBytes(t, plainRead, []byte("one\rtwo"))
	// Same-time requests exhaust the burst; a rate-limited id is not recorded, so once the limit
	// lifts its retry runs.
	for i := 0; i < 41; i++ {
		m.ID = fmt.Sprintf("rate%d", i)
		r = store.handle(route, m, now.Add(5*time.Minute))
		if (i < 40) != (r.Status == "ok") {
			t.Fatalf("rate %d: %+v", i, r)
		}
	}
	if got := store.handle(route, m, now.Add(6*time.Minute)); got.Status != "ok" {
		t.Fatalf("a rate-limited input's retry did not run: %+v", got)
	}
	if r := store.handle("unknown", original, now); r.Status != "refused" {
		t.Fatal(r)
	}
	if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := store.handle(route, original, now); r.Status != "refused" {
		t.Fatal("revoked duplicate", r)
	}
}

func TestRemoteChatSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, route := remoteInputFixture(t)
	m := remote.Message{Type: "chat_send", ID: "chat-once", Text: "hello from phone"}
	first := store.handle(route, m, time.Now())
	if first.Status != "ok" {
		t.Fatal(first)
	}
	if got := store.handle(route, m, time.Now()); got != first {
		t.Fatal(got)
	}
	msgs := readChat()
	if len(msgs) != 1 || msgs[0].From != "you" || msgs[0].Name != "you" || msgs[0].Text != m.Text {
		t.Fatalf("chat: %+v", msgs)
	}
}

func TestRemoteInputEncryptedDispatch(t *testing.T) {
	tmuxTestServer(t, "ptln-test-remote-input-wire")
	store, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]remotePhone{{ID: route, Static: phone.Public}})
	if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	channel := newRemoteChannel()
	channel.inputs = store
	command := remote.Message{Type: "input", ID: "over-reconnect", Pane: pane, Kind: "paste", Data: "once"}
	for attempt := 0; attempt < 2; attempt++ {
		channel.drop(route)
		h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
		if err != nil {
			t.Fatal(err)
		}
		hello, _, _, _ := h.WriteMessage(nil, nil)
		if err := channel.receive(route, hello, time.Now()); err != nil {
			t.Fatal(err)
		}
		_, send, receive, err := h.ReadMessage(nil, channel.pending[route])
		if err != nil {
			t.Fatal(err)
		}
		p := channel.peers[route]
		for _, m := range []remote.Message{command, {Type: "input", ID: fmt.Sprintf("refusal%d", attempt), Pane: "%999999", Kind: "key", Data: "Enter"}} {
			plain, _ := json.Marshal([]remote.Message{m})
			wire, _ := send.Encrypt(nil, nil, plain)
			if err := channel.receive(route, wire, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		wire, err := p.Flush()
		if err != nil {
			t.Fatal(err)
		}
		plain, err := receive.Decrypt(nil, nil, wire)
		if err != nil {
			t.Fatal(err)
		}
		var results []remote.Message
		if err := json.Unmarshal(plain, &results); err != nil {
			t.Fatal(err)
		}
		statuses := map[string]string{}
		for _, r := range results {
			if r.Type == "input_result" {
				statuses[r.ID] = r.Status
			}
		}
		if statuses[command.ID] != "ok" || statuses[fmt.Sprintf("refusal%d", attempt)] != "refused" {
			t.Fatalf("results: %s", plain)
		}

		if attempt == 1 {
			if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), []byte("[]"), 0600); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal([]remote.Message{{Type: "input", ID: "revoked", Pane: pane, Kind: "text", Data: "must not arrive"}})
			encrypted, _ := send.Encrypt(nil, nil, raw)
			if err := channel.receive(route, encrypted, time.Now()); err == nil || channel.peers[route] != nil {
				t.Fatal("revoked encrypted input accepted")
			}
			if err := channel.receive("unknown", encrypted, time.Now()); err == nil {
				t.Fatal("unknown phone accepted")
			}
		}
	}
	waitRemoteBytes(t, read, []byte("\x1b[200~once\x1b[201~"))
}

func TestRemoteInputHistoryEviction(t *testing.T) {
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-remote-input-history")
	t.Setenv("HOME", t.TempDir())
	store, route := remoteInputFixture(t)
	now := time.Now()
	first := remote.Message{Type: "input", ID: "old", Pane: "%99999", Kind: "key", Data: "F1"}
	original := store.handle(route, first, now)
	// The first refusal is immutable, even when the same id carries other data.
	changed := first
	changed.Data = "Escape"
	if got := store.handle(route, changed, now.Add(time.Second)); got != original {
		t.Fatal("duplicate changed its recorded result")
	}
	for i := 0; i < 1000; i++ {
		m := first
		m.ID = fmt.Sprint(i)
		store.handle(route, m, now.Add(time.Duration(i+2)*time.Second))
	}
	h := store.phones[route]
	if len(h.results) != 1000 || len(h.order) != 1000 {
		t.Fatal("unbounded history")
	}
	if _, ok := h.results[first.ID]; ok {
		t.Fatal("oldest id not evicted")
	}
}

// A rate-limit refusal is not the input's final answer: nothing ran, so the same id may be retried
// once the limit lifts and then runs.
func TestRemoteInputRateLimitRetry(t *testing.T) {
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-remote-input-ratelimit")
	t.Setenv("HOME", t.TempDir())
	store, route := remoteInputFixture(t)
	now := time.Now()
	// Exhaust the bucket with refusals that cost a token but run nothing.
	for i := 0; i < 40; i++ {
		store.handle(route, remote.Message{Type: "input", ID: fmt.Sprint("burn", i), Pane: "%99999", Kind: "key", Data: "F1"}, now)
	}
	m := remote.Message{Type: "input", ID: "retry-me", Pane: "%99999", Kind: "key", Data: "Escape"}
	if got := store.handle(route, m, now); got.Reason != "rate limit exceeded" {
		t.Fatalf("not rate limited: %+v", got)
	}
	// A second later the bucket has refilled: the same id is evaluated again, not answered from history.
	if got := store.handle(route, m, now.Add(time.Second)); got.Reason == "rate limit exceeded" {
		t.Fatalf("the retry got the recorded rate-limit refusal: %+v", got)
	}
}
