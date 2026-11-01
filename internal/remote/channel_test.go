package remote

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/flynn/noise"
)

func channelPeers(t *testing.T) (*Peer, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	a, b, _ := noisePeers(t, "KK", false, nil)
	wire, _, _, err := a.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = b.ReadMessage(nil, wire); err != nil {
		t.Fatal(err)
	}
	wire, receive, send, err := b.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, phoneSend, phoneReceive, err := a.ReadMessage(nil, wire)
	if err != nil {
		t.Fatal(err)
	}
	return NewPeer(send, receive), phoneSend, phoneReceive
}
func TestRemoteChannelFragmentation(t *testing.T) {
	p, send, receive := channelPeers(t)
	now := time.Now()
	raw, _ := json.Marshal([]Message{{Type: "sub", Pane: "chat", Mode: "focus"}})
	wire, _ := send.Encrypt(nil, nil, raw)
	if err := p.Read(wire, now); err != nil {
		t.Fatal(err)
	}
	text := string(bytes.Repeat([]byte("private chat entry\n"), 8000))
	if err := p.Collect(now, 0, func(string, string) (any, error) { return text, nil }); err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(); err != nil {
		t.Fatal(err)
	}
	var assembled []byte
	index := 0
	for p.Size() > 0 {
		size := p.Size()
		wire, err := p.Flush()
		if err != nil {
			t.Fatal(err)
		}
		if size != len(wire) || size > 65515 {
			t.Fatal("invalid transport size")
		}
		plain, err := receive.Decrypt(nil, nil, wire)
		if err != nil {
			t.Fatal(err)
		}
		var fragments []struct {
			Type             string
			ID, Index, Parts int
			Data             string
		}
		if err = json.Unmarshal(plain, &fragments); err != nil {
			t.Fatal(err)
		}
		if len(fragments) != 1 || fragments[0].Type != "fragment" || fragments[0].Index != index {
			t.Fatal("invalid fragment")
		}
		chunk, err := base64.StdEncoding.DecodeString(fragments[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, chunk...)
		index++
	}
	var messages []Message
	if err := json.Unmarshal(assembled, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Data != text || index < 2 {
		t.Fatal("fragmented snapshot lost data")
	}
	// Postponed messages and remaining fragments must not escape after expiry.
	if err := p.Collect(now.Add(time.Second), 0, func(string, string) (any, error) { return text + "changed", nil }); err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(); err != nil {
		t.Fatal(err)
	}
	wire, _ = p.Flush()
	if _, err := receive.Decrypt(nil, nil, wire); err != nil {
		t.Fatal(err)
	}
	_ = p.Collect(now.Add(60*time.Second), 0, func(string, string) (any, error) { t.Fatal("captured expired lease"); return nil, nil })
	wire, err := p.Flush()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := receive.Decrypt(nil, nil, wire)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "[{\"type\":\"reset\"}]" {
		t.Fatal("stale fragments leaked", string(plain))
	}
	if wire, _ = p.Flush(); len(wire) != 0 {
		t.Fatal("stale pending snapshot")
	}
}

// An input's result is sent ahead of a snapshot already queued as fragments: behind them it
// outlived the phone's wait for it. The fragments then continue in order.
func TestRemoteInputResultGoesFirst(t *testing.T) {
	p, send, receive := channelPeers(t)
	now := time.Now()
	read := func(m ...Message) {
		raw, _ := json.Marshal(m)
		wire, _ := send.Encrypt(nil, nil, raw)
		if err := p.Read(wire, now); err != nil {
			t.Fatal(err)
		}
	}
	read(Message{Type: "sub", Pane: "%21", Mode: "focus"})
	// Incompressible, so the zstd'd snapshot still needs fragments.
	noise := make([]byte, 120000)
	for i, x := 0, uint32(1); i < len(noise); i++ {
		x = x*1664525 + 1013904223
		noise[i] = byte(x >> 24)
	}
	text := base64.StdEncoding.EncodeToString(noise)
	if err := p.Collect(now, 0, func(string, string) (any, error) { return text, nil }); err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(); err != nil {
		t.Fatal(err)
	}
	next := func() []map[string]any {
		t.Helper()
		size := p.Size()
		wire, err := p.Flush()
		if err != nil || len(wire) != size {
			t.Fatalf("flush: %v, size %d vs %d", err, len(wire), size)
		}
		plain, err := receive.Decrypt(nil, nil, wire)
		if err != nil {
			t.Fatal(err)
		}
		var ms []map[string]any
		_ = json.Unmarshal(plain, &ms)
		return ms
	}
	if first := next(); first[0]["type"] != "fragment" || first[0]["index"].(float64) != 0 {
		t.Fatalf("first frame: %v", first[0]["type"])
	}
	p.Command = func(m Message, _ time.Time) Message { return Message{Type: "input_result", ID: m.ID, Status: "ok"} }
	read(Message{Type: "input", ID: "in-1", Pane: "%21", Kind: "key", Data: "Enter"})
	if got := next(); len(got) != 1 || got[0]["type"] != "input_result" || got[0]["id"] != "in-1" {
		t.Fatalf("the result did not go first: %v", got)
	}
	// A text input's final result and its delivery outcome arrive later, from a worker: they also
	// go ahead of the fragments, in order.
	p.QueueControl(Message{Type: "input_result", ID: "in-2", Status: "ok"}, Message{Type: "input_delivery", ID: "in-2", Status: "ok"})
	if got := next(); len(got) != 2 || got[0]["type"] != "input_result" || got[1]["type"] != "input_delivery" {
		t.Fatalf("a worker's results did not go first, in order: %v", got)
	}
	for i := 1; p.Size() > 0; i++ {
		got := next()
		if got[0]["type"] != "fragment" || int(got[0]["index"].(float64)) != i {
			t.Fatalf("fragment %d out of order: %v %v", i, got[0]["type"], got[0]["index"])
		}
	}
}
