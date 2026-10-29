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
