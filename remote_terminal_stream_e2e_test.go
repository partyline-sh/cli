package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

func TestEncryptedPhonePTYBypassesSnapshotTick(t *testing.T) {
	tmuxTestServer(t, "ptln-test-stream-encrypted")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	defer exec.Command("tmux", "-L", phoneTerminalRegistry().Socket, "kill-server").Run()
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
	phones, _ := json.Marshal([]remotePhone{{ID: route, Static: phone.Public}})
	if err = os.WriteFile(filepath.Join(relayDir(), "phones.json"), phones, 0600); err != nil {
		t.Fatal(err)
	}
	c := newRemoteChannel()
	defer func() {
		if c.term != nil {
			c.term.Close()
		}
	}()
	handshake, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
	if err != nil {
		t.Fatal(err)
	}
	first, _, _, err := handshake.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = c.receive(route, first, now); err != nil {
		t.Fatal(err)
	}
	extract := func(batch []byte) []byte {
		t.Helper()
		if len(batch) < 20 {
			t.Fatal("missing transport batch")
		}
		n := int(binary.BigEndian.Uint32(batch[16:20]))
		if n+20 > len(batch) {
			t.Fatal("invalid batch")
		}
		return batch[20 : 20+n]
	}
	batch, err := c.batch(now)
	if err != nil {
		t.Fatal(err)
	}
	_, send, receive, err := handshake.ReadMessage(nil, extract(batch))
	if err != nil {
		t.Fatal(err)
	}
	submit := func(msg remote.Message) {
		t.Helper()
		raw, _ := json.Marshal([]remote.Message{msg})
		wire, e := send.Encrypt(nil, nil, raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = c.receive(route, wire, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	// No batch/tick call occurs after this point, and even normal snapshots are
	// in their slowest budget tier. Both open and output must use the event lane.
	c.last = time.Now()
	c.count = 80000
	submit(remote.Message{Type: "term_open", ID: "encrypted-create", Data: map[string]int{"columns": 80, "rows": 24}})
	next := func() remote.Message {
		t.Helper()
		select {
		case event := <-c.termEvents:
			wire, e := c.terminalFrame(event)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := receive.Decrypt(nil, nil, extract(wire))
			if e != nil {
				t.Fatal(e)
			}
			var messages []remote.Message
			if e = json.Unmarshal(raw, &messages); e != nil || len(messages) != 1 {
				t.Fatal(string(raw), e)
			}
			return messages[0]
		case <-time.After(5 * time.Second):
			t.Fatal("stream waited for snapshot tick")
			return remote.Message{}
		}
	}
	opened := next()
	if opened.Type != "term_result" || opened.Status != "ok" {
		t.Fatal(opened)
	}
	data := opened.Data.(map[string]any)
	epoch := data["attachment"].(string)
	session := data["session"].(string)
	command := "printf 'E2E-%s\\n' verified\r"
	submit(remote.Message{Type: "term_input", ID: epoch, Pane: session, Revision: 1, Data: base64.StdEncoding.EncodeToString([]byte(command))})
	var output strings.Builder
	for !strings.Contains(output.String(), "E2E-verified") {
		msg := next()
		if msg.Type == "term_output" {
			raw, e := base64.StdEncoding.DecodeString(msg.Data.(string))
			if e != nil {
				t.Fatal(e)
			}
			output.Write(raw)
			submit(remote.Message{Type: "term_ack", ID: epoch, Pane: session, Revision: msg.Revision})
		}
	}
	if c.last.Before(now) {
		t.Fatal("invalid fixture clock")
	}
}
