package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

func TestRemoteTranscriptParsers(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		t.Run(engine, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", "transcript", engine+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			entries, err := readTranscript(f, engine)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) < 2 || entries[0].Role != "user" || entries[1].Role != "assistant" || entries[1].Text != "First line\nsecond line\nAnother block" {
				t.Fatalf("entries: %+v", entries)
			}
			var tools []transcriptTool
			for _, e := range entries {
				tools = append(tools, e.Tools...)
			}
			if len(tools) != 3 {
				t.Fatalf("tools: %+v", tools)
			}
			for i, status := range []string{"ok", "error", "running"} {
				if tools[i].Status != status || tools[i].Name == "" || tools[i].Target == "" {
					t.Fatalf("tool: %+v", tools[i])
				}
			}
			if tools[0].Target != "go test ./..." {
				t.Fatal(tools[0])
			}
			if engine == "claude" && (tools[1].Target != "missing.go" || tools[2].Target != "main.go") {
				t.Fatal(tools)
			}
		})
	}
	if transcriptTarget("Other", json.RawMessage(`{"z":"first","a":"second"}`)) != "first" {
		t.Fatal("fallback argument order")
	}
	for _, output := range []string{`{"metadata":{"exit_code":0},"output":"example: exit code 1"}`, `"The docs mention exit code 2"`} {
		if transcriptOutputStatus(json.RawMessage(output)) != "ok" {
			t.Fatal(output)
		}
	}
	for _, output := range []string{`"Process exited with code 2"`, `{"metadata":{"exit_code":3}}`, `"{\"exit_code\":1}"`} {
		if transcriptOutputStatus(json.RawMessage(output)) != "error" {
			t.Fatal(output)
		}
	}
}

func TestRemoteTranscriptBounds(t *testing.T) {
	var input bytes.Buffer
	for i := 0; i < 250; i++ {
		b, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "assistant", "content": strings.Repeat("\x00界", 5000)}})
		input.Write(b)
		input.WriteByte('\n')
	}
	entries, err := readTranscript(&input, "claude")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(entries)
	envelope, _ := json.Marshal([]remote.Message{{Type: "transcript:" + strings.Repeat("x", 128), Revision: 999999, Data: entries}})
	if len(envelope)+16 > remote.MaxCiphertextBytes {
		t.Fatal("snapshot exceeds encrypted transport bound")
	}
	if len(entries) == 0 || len(entries) > 200 || len(b) > transcriptSnapshotLimit || !strings.HasSuffix(entries[len(entries)-1].Text, "[cut]") {
		t.Fatalf("bounds: entries=%d bytes=%d", len(entries), len(b))
	}
	input.Reset()
	for i := 0; i < 250; i++ {
		input.WriteString(`{"message":{"role":"user","content":"hello"}}` + "\n")
	}
	entries, err = readTranscript(&input, "claude")
	if err != nil || len(entries) != 200 {
		t.Fatalf("count: %d %v", len(entries), err)
	}
}

func TestRemoteTranscriptIdentity(t *testing.T) {
	sessions := []aiSession{{ID: "abc", Tool: "claude", storePath: "fixture"}}
	if transcriptSession("abc", "claude", sessions) == nil {
		t.Fatal("exact ID not matched")
	}
	for _, key := range []string{"new-claude-123", "missing", ""} {
		if transcriptSession(key, "claude", sessions) != nil {
			t.Fatal(key)
		}
	}
	if transcriptSession("abc", "gemini", sessions) != nil {
		t.Fatal("unsupported parser")
	}
	sessions = append(sessions, aiSession{ID: "abc", Tool: "claude", storePath: "other"})
	if transcriptSession("abc", "claude", sessions) != nil {
		t.Fatal("ambiguous match")
	}
}

func TestRemoteTranscriptInventory(t *testing.T) {
	tmuxTestServer(t, "ptln-test-remote-transcript")
	t.Setenv("HOME", t.TempDir())
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "sleep", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pane := strings.TrimSpace(string(out))
	if _, err = remoteSnapshot("transcript:%99999", "focus"); err == nil {
		t.Fatal("outside inventory allowed")
	}
	for _, key := range []string{"", "new-claude-123"} {
		if out, err = tmuxCmd("set-option", "-p", "-t", pane, "@ptln_key", key).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		data, err := remoteSnapshot("transcript:"+pane, "focus")
		if err != nil || data != nil {
			t.Fatalf("fallback: %v %v", data, err)
		}
	}
}

// Use the production encrypted peer; no live tmux or transcript stores.
func transcriptPeer(t *testing.T) (*remote.Peer, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	a, _ := noise.DH25519.GenerateKeypair(rand.Reader)
	b, _ := noise.DH25519.GenerateKeypair(rand.Reader)
	phone, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: a, PeerStatic: b.Public})
	if err != nil {
		t.Fatal(err)
	}
	machine, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Static: b, PeerStatic: a.Public})
	if err != nil {
		t.Fatal(err)
	}
	first, _, _, _ := phone.WriteMessage(nil, nil)
	if _, _, _, err = machine.ReadMessage(nil, first); err != nil {
		t.Fatal(err)
	}
	reply, receive, send, err := machine.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, ps, pr, err := phone.ReadMessage(nil, reply)
	if err != nil {
		t.Fatal(err)
	}
	return remote.NewPeer(send, receive), ps, pr
}

func TestRemoteTranscriptRevisionsAndIsolation(t *testing.T) {
	for peerIndex := 0; peerIndex < 2; peerIndex++ {
		p, send, receive := transcriptPeer(t)
		now := time.Now()
		request := func(ms ...remote.Message) {
			t.Helper()
			b, _ := json.Marshal(ms)
			wire, _ := send.Encrypt(nil, nil, b)
			if err := p.Read(wire, now); err != nil {
				t.Fatal(err)
			}
		}
		request(remote.Message{Type: "sub", Pane: "transcript:%1", Mode: "focus"}, remote.Message{Type: "sub", Pane: "transcript:%2", Mode: "focus"}, remote.Message{Type: "sub", Pane: "board", Mode: "focus"})
		text := "one"
		bad := false
		snapshot := func(topic, mode string) (any, error) {
			switch topic {
			case "transcript:%2":
				return nil, errors.New("broken transcript")
			case "transcript:%1":
				if bad {
					return make(chan int), nil
				}
				return []transcriptEntry{{Role: "assistant", Text: text, Tools: []transcriptTool{}}}, nil
			default:
				return []string{"board"}, nil
			}
		}
		collect := func() []remote.Message {
			t.Helper()
			if err := p.Collect(now, 0, snapshot); err != nil {
				t.Fatal(err)
			}
			wire, err := p.Flush()
			if err != nil {
				t.Fatal(err)
			}
			if len(wire) == 0 {
				return nil
			}
			plain, err := receive.Decrypt(nil, nil, wire)
			if err != nil {
				t.Fatal(err)
			}
			var ms []remote.Message
			if err = json.Unmarshal(plain, &ms); err != nil {
				t.Fatal(err)
			}
			return ms
		}
		ms := collect()
		if len(ms) != 3 {
			t.Fatalf("topics: %+v", ms)
		}
		for _, m := range ms {
			if strings.HasPrefix(m.Type, "transcript:") && (m.Encoding != "" || m.Revision != 1) {
				t.Fatal(m)
			}
		}
		text = "two\nnew line"
		now = now.Add(time.Second)
		ms = collect()
		if len(ms) != 1 || ms[0].Type != "transcript:%1" || ms[0].Revision != 2 {
			t.Fatalf("revision: %+v", ms)
		}
		now = now.Add(time.Second)
		request(remote.Message{Type: "resync", Pane: "transcript:%1"})
		ms = collect()
		if len(ms) != 1 || ms[0].Revision != 3 {
			t.Fatalf("resync: %+v", ms)
		}
		bad = true
		now = now.Add(time.Second)
		request(remote.Message{Type: "resync", Pane: "board"})
		ms = collect()
		if len(ms) != 1 || ms[0].Type != "board" {
			t.Fatalf("marshal failure isolation: %+v", ms)
		}
	}
}

func TestRemoteTranscriptFileAppends(t *testing.T) {
	tmuxTestServer(t, "ptln-test-remote-transcript-file")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/transcript/claude.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native-id.jsonl")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	pane := openTagged(t, Spec{Key: "native-id", Engine: "claude", Argv: []string{"claude", "--resume", "native-id"}})
	topic := "transcript:" + pane
	snapshot, err := remoteSnapshot(topic, "focus")
	if err != nil || len(snapshot.([]transcriptEntry)) != 2 {
		t.Fatalf("discovery: %v %v", snapshot, err)
	}
	p, send, receive := transcriptPeer(t)
	now := time.Now()
	raw, _ := json.Marshal([]remote.Message{{Type: "sub", Pane: topic, Mode: "focus"}})
	wire, _ := send.Encrypt(nil, nil, raw)
	if err = p.Read(wire, now); err != nil {
		t.Fatal(err)
	}
	collect := func() []remote.Message {
		t.Helper()
		if err := p.Collect(now, 0, remoteSnapshot); err != nil {
			t.Fatal(err)
		}
		wire, err := p.Flush()
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) == 0 {
			return nil
		}
		raw, err := receive.Decrypt(nil, nil, wire)
		if err != nil {
			t.Fatal(err)
		}
		var ms []remote.Message
		if err = json.Unmarshal(raw, &ms); err != nil {
			t.Fatal(err)
		}
		return ms
	}
	if ms := collect(); len(ms) != 1 || ms[0].Revision != 1 {
		t.Fatalf("initial: %+v", ms)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(`{"message":{"role":"assistant","content":"new`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if ms := collect(); len(ms) != 0 {
		t.Fatalf("partial line emitted: %+v", ms)
	}
	if _, err = f.WriteString("\\nreply\"}}\n"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	ms := collect()
	if len(ms) != 1 || ms[0].Revision != 2 || !strings.Contains(string(mustTranscriptJSON(t, ms[0].Data)), `new\nreply`) {
		t.Fatalf("append: %+v", ms)
	}
	// A matching result changes the retained tool in a later revision.
	if _, err = f.WriteString(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"pending"}]}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	ms = collect()
	if len(ms) != 1 || ms[0].Revision != 3 {
		t.Fatalf("result revision: %+v", ms)
	}
	var parsed []transcriptEntry
	if err = json.Unmarshal(mustTranscriptJSON(t, ms[0].Data), &parsed); err != nil || parsed[1].Tools[2].Status != "ok" {
		t.Fatalf("live tool result: %+v %v", parsed, err)
	}
	// Fresh keys must not borrow the existing file in the same directory.
	fresh := openTagged(t, Spec{Key: "new-claude-123", Engine: "claude", Argv: []string{"claude"}})
	if data, err := remoteSnapshot("transcript:"+fresh, "focus"); data != nil || err != nil {
		t.Fatalf("fresh: %v %v", data, err)
	}
	// Rotation/truncation resets the cached parser window.
	if err = os.WriteFile(path, []byte(`{"message":{"role":"user","content":"replacement"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := cachedTranscript(path, "claude")
	if err != nil || len(entries) != 1 || entries[0].Text != "replacement" {
		t.Fatalf("truncate: %+v %v", entries, err)
	}
}
func mustTranscriptJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
