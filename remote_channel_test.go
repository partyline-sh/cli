package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

func TestRemoteChannel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	config, configErr := newRelayConfig("https://private.example.test", strings.Repeat("1", 32))
	if configErr != nil {
		t.Fatal(configErr)
	}
	config.Hosting = "self-hosted"
	if configErr = saveRelay(config, strings.Repeat("2", 64)); configErr != nil {
		t.Fatal(configErr)
	}
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	route := "11223344556677889900112233445566"
	phones := []remotePhone{{ID: route, Static: phone.Public}}
	save := func() {
		t.Helper()
		b, _ := json.Marshal(phones)
		if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	now := time.Now()
	channel := newRemoteChannel()
	h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
	if err != nil {
		t.Fatal(err)
	}
	first, _, _, _ := h.WriteMessage(nil, nil)
	if err = channel.receive(route, first, now); err != nil {
		t.Fatal(err)
	}
	extract := func(b []byte) []byte {
		t.Helper()
		if len(b) < 20 {
			t.Fatalf("short batch %x", b)
		}
		n := int(binary.BigEndian.Uint32(b[16:20]))
		if n+20 > len(b) {
			t.Fatal("bad entry")
		}
		return b[20 : 20+n]
	}
	batch, err := channel.batch(now)
	if err != nil {
		t.Fatal(err)
	}
	_, send, receive, err := h.ReadMessage(nil, extract(batch))
	if err != nil {
		t.Fatal(err)
	}
	request := func(messages ...remote.Message) []byte {
		t.Helper()
		b, _ := json.Marshal(messages)
		wire, e := send.Encrypt(nil, nil, b)
		if e != nil {
			t.Fatal(e)
		}
		if e = channel.receive(route, wire, now); e != nil {
			t.Fatal(e)
		}
		return wire
	}
	request(remote.Message{Type: "sub", Pane: "board", Mode: "focus"}, remote.Message{Type: "sub", Pane: "contractors", Mode: "focus"}, remote.Message{Type: "sub", Pane: "chat", Mode: "focus"})
	decode := func(b []byte) []remote.Message {
		t.Helper()
		plain, e := receive.Decrypt(nil, nil, extract(b))
		if e != nil {
			t.Fatal(e)
		}
		var ms []remote.Message
		if e = json.Unmarshal(plain, &ms); e != nil {
			t.Fatal(e)
		}
		return ms
	}
	batch, err = channel.batch(now.Add(500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	messages := decode(batch)

	for _, message := range messages {
		if message.Type == "hello" {
			info, ok := message.Data.(map[string]any)
			if !ok {
				t.Fatal("hello is not an object")
			}
			route, ok := info["relay"].(map[string]any)
			if !ok || len(route) != 2 || route["origin"] != config.URL || route["hosting"] != "self-hosted" {
				t.Fatal("encrypted hello missing public route", route)
			}
		}
	}
	for _, topic := range []string{"hello", "sessions", "gates", "board", "contractors", "chat"} {
		found := false
		for _, m := range messages {
			if m.Type == topic {
				found = true
			}
		}
		if !found {
			t.Fatal("missing", topic)
		}
	}
	// Mutate the real stores and require the real machine adapter to emit changes.
	run := &loopRun{ID: "a1b2c3", State: loopAwaiting, Note: "new board"}
	if err = run.save(loopEvent{}); err != nil {
		t.Fatal(err)
	}
	contractor := &contractor{ID: "abcdef12345678", Title: "new contractor", State: "running"}
	if err = contractor.save(); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(chatDir(), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(chatMessage{Seq: 1, Text: "new chat"})
	if err = os.WriteFile(chatLogPath(), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err = channel.batch(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	messages = decode(batch)
	for _, topic := range []string{"board", "contractors", "chat"} {
		found := false
		for _, m := range messages {
			if m.Type == topic {
				found = true
			}
		}
		if !found {
			t.Fatal("missing change", topic)
		}
	}
	if b, _ := channel.batch(now.Add(1100 * time.Millisecond)); len(b) != 0 {
		t.Fatal("multiple frames in window")
	}
	if b, _ := channel.batch(now.Add(60 * time.Second)); len(b) != 0 {
		t.Fatal("lapsed lease sent output")
	}
	// Replayed authenticated transport must fail and remove the session.
	now = now.Add(61 * time.Second)
	replay := request(remote.Message{Type: "sub", Pane: "board", Mode: "focus"})
	if err = channel.receive(route, replay, now); err == nil || channel.peers[route] != nil {
		t.Fatal("replay accepted")
	}
	// Reconnect uses fresh ephemeral keys; a revoked projection closes its session.
	h, _ = remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
	second, _, _, _ := h.WriteMessage(nil, nil)
	if bytes.Equal(first, second) {
		t.Fatal("ephemeral reused")
	}
	if err = channel.receive(route, second, now); err != nil {
		t.Fatal(err)
	}
	phones = nil
	save()
	_, _ = channel.batch(now.Add(5 * time.Second))
	if channel.peers[route] != nil {
		t.Fatal("revoked session open")
	}
	t.Run("rate and pane batching", func(t *testing.T) {
		a, _ := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
		b, _ := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Static: machine, PeerStatic: phone.Public})
		m, _, _, _ := a.WriteMessage(nil, nil)
		_, _, _, _ = b.ReadMessage(nil, m)
		m, rc, sc, _ := b.WriteMessage(nil, nil)
		_, cs, cr, _ := a.ReadMessage(nil, m)
		p := remote.NewPeer(sc, rc)
		raw, _ := json.Marshal([]remote.Message{{Type: "sub", Pane: "%1", Mode: "focus"}, {Type: "sub", Pane: "%2", Mode: "focus"}, {Type: "sub", Pane: "%3", Mode: "summary"}})
		wire, _ := cs.Encrypt(nil, nil, raw)
		if err := p.Read(wire, now); err != nil {
			t.Fatal(err)
		}
		captures := 0
		snapshot := func(string, string) (any, error) { captures++; return "screen text", nil }
		if err := p.Collect(now, 0, snapshot); err != nil {
			t.Fatal(err)
		}
		out, err := p.Flush()
		if err != nil {
			t.Fatal(err)
		}
		plain, err := cr.Decrypt(nil, nil, out)
		if err != nil {
			t.Fatal(err)
		}
		var ms []remote.Message
		_ = json.Unmarshal(plain, &ms)
		if len(ms) != 3 {
			t.Fatal("panes not batched", string(plain))
		}
		captures = 0
		_ = p.Collect(now.Add(500*time.Millisecond), 50000, snapshot)
		if captures != 0 {
			t.Fatal("50% budget rate")
		}
		_ = p.Collect(now.Add(time.Second), 50000, snapshot)
		if captures != 2 {
			t.Fatal("focus must resume at 1s")
		}
		captures = 0
		_ = p.Collect(now.Add(4*time.Second), 80000, snapshot)
		if captures != 0 {
			t.Fatal("80% budget rate")
		}
		_ = p.Collect(now.Add(6*time.Second), 80000, snapshot)
		if captures != 2 {
			t.Fatal("summaries not stopped")
		}
		captures = 0
		_ = p.Collect(now.Add(60*time.Second), 0, snapshot)
		if captures != 0 {
			t.Fatal("expired panes captured")
		}
		for count, want := range map[int]time.Duration{0: 500 * time.Millisecond, 50000: time.Second, 80000: 5 * time.Second} {
			if remote.Interval(count) != want {
				t.Fatal(count)
			}
		}
	})
}

func TestRemoteChannelSeveralPhones(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c := newRemoteChannel()
	phones := []remotePhone{}
	initiators := map[string]*noise.HandshakeState{}
	first := map[string][]byte{}
	for i := 0; i < 4; i++ {
		key, err := noise.DH25519.GenerateKeypair(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		route := strings.Repeat(string(rune('1'+i)), 32)
		phones = append(phones, remotePhone{ID: route, Static: key.Public})
		h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: key, PeerStatic: machine.Public})
		if err != nil {
			t.Fatal(err)
		}
		initiators[route] = h
		first[route], _, _, _ = h.WriteMessage(nil, nil)
	}
	raw, _ := json.Marshal(phones)
	if err = os.WriteFile(filepath.Join(relayDir(), "phones.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for route, b := range first {
		if err = c.receive(route, b, now); err != nil {
			t.Fatal(err)
		}
	}
	split := func(batch []byte) map[string][]byte {
		t.Helper()
		out := map[string][]byte{}
		for len(batch) > 0 {
			if len(batch) < 20 {
				t.Fatal("truncated batch")
			}
			route := hex.EncodeToString(batch[:16])
			n := int(binary.BigEndian.Uint32(batch[16:20]))
			if n+20 > len(batch) {
				t.Fatal("truncated ciphertext")
			}
			out[route] = batch[20 : 20+n]
			batch = batch[20+n:]
		}
		return out
	}
	batch, err := c.batch(now)
	if err != nil {
		t.Fatal(err)
	}
	entries := split(batch)
	if len(entries) != 4 {
		t.Fatal("phones not in one frame")
	}
	receivers := map[string]*noise.CipherState{}
	for route, wire := range entries {
		_, send, receive, err := initiators[route].ReadMessage(nil, wire)
		if err != nil {
			t.Fatal(err)
		}
		receivers[route] = receive
		plain, _ := json.Marshal([]remote.Message{{Type: "sub", Pane: "board", Mode: "focus"}})
		wire, err = send.Encrypt(nil, nil, plain)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.receive(route, wire, now); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := c.batch(now.Add(499 * time.Millisecond)); len(b) > 0 {
		t.Fatal("extra frame in window")
	}
	batch, err = c.batch(now.Add(500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	entries = split(batch)
	if len(entries) != 4 {
		t.Fatal("output was not batched across phones")
	}
	for route, wire := range entries {
		if _, err = receivers[route].Decrypt(nil, nil, wire); err != nil {
			t.Fatal("nonce skipped", err)
		}
	}
}

func TestRemoteChannelPaneCapture(t *testing.T) {
	tmuxTestServer(t, "ptln-remote-channel-test")
	pane := openTagged(t, Spec{Key: "remote-pane", Label: "Remote Pane", Engine: "codex", Argv: []string{"sleep", "300"}})
	// The real adapter must use the tmux pane, and refuse arbitrary tmux targets.
	screen, err := remoteSnapshot(pane, "focus")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := screen.(string); !ok {
		t.Fatal("screen is not a snapshot")
	}
	inventory, err := remoteSnapshot("sessions", "focus")
	if err != nil {
		t.Fatal(err)
	}
	rows := inventory.([]map[string]string)
	if len(rows) != 1 || rows[0]["engine"] != "codex" || rows[0]["title"] != "Remote Pane" {
		t.Fatal("invalid session metadata", rows)
	}
	if _, err = remoteSnapshot("partyline:0", "focus"); err == nil {
		t.Fatal("arbitrary tmux target accepted")
	}
}

// TestRemoteChannelBye isolates the transport from tmux and the snapshot adapter.
func TestRemoteChannelBye(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	c := newRemoteChannel()
	now := time.Now()
	receivers := map[string]*noise.CipherState{}
	phones := []remotePhone{}
	for _, route := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		machine, _ := noise.DH25519.GenerateKeypair(rand.Reader)
		phone, _ := noise.DH25519.GenerateKeypair(rand.Reader)
		a, _ := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
		b, _ := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Static: machine, PeerStatic: phone.Public})
		first, _, _, _ := a.WriteMessage(nil, nil)
		if _, _, _, err := b.ReadMessage(nil, first); err != nil {
			t.Fatal(err)
		}
		reply, receive, send, _ := b.WriteMessage(nil, nil)
		_, _, receiver, err := a.ReadMessage(nil, reply)
		if err != nil {
			t.Fatal(err)
		}
		c.peers[route] = remote.NewPeer(send, receive)
		receivers[route] = receiver
		phones = append(phones, remotePhone{ID: route, Static: phone.Public})
	}
	raw, _ := json.Marshal(phones)
	if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	bad, good := phones[0].ID, phones[1].ID
	if err := c.receive(bad, []byte("unreadable"), now); err == nil {
		t.Fatal("accepted invalid ciphertext")
	}
	if c.peers[bad] != nil || c.peers[good] == nil {
		t.Fatal("wrong peers dropped")
	}
	c.peers[good].Queue(remote.Message{Type: "hello"})
	batch, err := c.batch(now)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(batch) > 0 {
		route := hex.EncodeToString(batch[:16])
		n := int(binary.BigEndian.Uint32(batch[16:20]))
		plain, err := receivers[route].Decrypt(nil, nil, batch[20:20+n])
		if err != nil {
			t.Fatal(err)
		}
		var ms []remote.Message
		if err := json.Unmarshal(plain, &ms); err != nil {
			t.Fatal(err)
		}
		if route == bad && (len(ms) != 1 || ms[0].Type != "bye" || ms[0].Reason == "") {
			t.Fatalf("missing bye: %s", plain)
		}
		seen[route] = true
		batch = batch[20+n:]
	}
	if !seen[bad] || !seen[good] {
		t.Fatal("missing phone output")
	}
	if len(c.ended) != 0 || c.peers[good] == nil {
		t.Fatal("terminal notice retained or healthy peer dropped")
	}
}

// The phone groups Loops cards by column name; the number was the board's internal order.
func TestRemoteBoardRowNamesItsColumn(t *testing.T) {
	for sec, want := range map[tabSection]string{tabNeedsYou: "Needs you", tabPlanned: "Planned", tabInProgress: "In progress", tabInReview: "In review", tabDone: "Done"} {
		b, _ := json.Marshal(remoteBoardRow(tabRow{Section: sec, RunID: "abc123", Round: 2}))
		var got struct {
			Section string `json:"section"`
			Round   int    `json:"round"`
		}
		if err := json.Unmarshal(b, &got); err != nil || got.Section != want || got.Round != 2 {
			t.Errorf("section %d: %s (%v)", sec, b, err)
		}
	}
}

// A phone gets the end of the room, not its whole history: at most 100 messages and 40 KB, the
// newest always included, oldest first.
func TestRemoteChatTail(t *testing.T) {
	var msgs []chatMessage
	for i := 1; i <= 250; i++ {
		msgs = append(msgs, chatMessage{Seq: i, Name: "you", Text: "short"})
	}
	if got := remoteChatTail(msgs); len(got) != 100 || got[0].Seq != 151 || got[99].Seq != 250 {
		t.Errorf("by count: %d messages, %d..%d", len(got), got[0].Seq, got[len(got)-1].Seq)
	}
	big := strings.Repeat("x", 9000)
	msgs = nil
	for i := 1; i <= 20; i++ {
		msgs = append(msgs, chatMessage{Seq: i, Text: big})
	}
	got := remoteChatTail(msgs)
	b, _ := json.Marshal(got)
	if len(got) != 4 || got[len(got)-1].Seq != 20 || len(b) > 41<<10 {
		t.Errorf("by size: %d messages ending %d, %d bytes", len(got), got[len(got)-1].Seq, len(b))
	}
	huge := []chatMessage{{Seq: 1, Text: "old"}, {Seq: 2, Text: strings.Repeat("y", 60000)}}
	if got := remoteChatTail(huge); len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("the newest message must be sent even when it alone is over the limit: %v", len(got))
	}
	if got := remoteChatTail(nil); len(got) != 0 {
		t.Errorf("empty room: %v", got)
	}
}

// A session outside the chat room, which the watcher does not probe, reports what its screen shows
// instead of idle.
func TestRemoteSessionStateOutsideTheRoom(t *testing.T) {
	tmuxTestServer(t, "ptln-remote-state-test")
	pane := openTagged(t, Spec{Key: "remote-busy", Label: "Busy", Engine: "codex"})
	if out, err := tmuxCmd("respawn-pane", "-k", "-t", pane, "sh", "-c", "echo 'Working (5s • esc to interrupt)'; sleep 300").CombinedOutput(); err != nil {
		t.Fatalf("respawn: %v %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		inventory, err := remoteSnapshot("sessions", "focus")
		if err != nil {
			t.Fatal(err)
		}
		rows := inventory.([]map[string]string)
		if len(rows) == 1 && rows[0]["state"] == "working" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state never became working: %v", rows)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
