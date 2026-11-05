package terminalstream

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/phoneterm"
	"partyline.sh/partyline/internal/remote"
)

func next(t *testing.T, m *Manager, want string) remote.Message {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-m.Events:
			if event.Message.Type == want {
				return event.Message
			}
		case <-deadline:
			t.Fatalf("missing %s", want)
		}
	}
}
func TestStreamOrderedBytesAndEpoch(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	r := phoneterm.Registry{Root: t.TempDir(), Socket: fmt.Sprintf("ptln-test-stream-%d", os.Getpid())}
	defer exec.Command("tmux", "-L", r.Socket, "kill-server").Run()
	m := New(r, nil)
	defer m.Close()
	if err := m.Submit("phone", remote.Message{Type: "term_open", ID: "request", Data: map[string]int{"columns": 80, "rows": 24}}); err != nil {
		t.Fatal(err)
	}
	opened := next(t, m, "term_result")
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	epoch := opened.Data.(map[string]any)["attachment"].(string)
	session := opened.Pane
	raw := []byte("printf 'STREAM-%s\\n' works\r")
	if err := m.Submit("phone", remote.Message{Type: "term_input", ID: epoch, Pane: session, Revision: 1, Data: base64.StdEncoding.EncodeToString(raw)}); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var seq uint64
	deadline := time.After(5 * time.Second)
	for !strings.Contains(text.String(), "STREAM-works") {
		select {
		case event := <-m.Events:
			msg := event.Message
			if msg.Type == "term_output" {
				if msg.ID != epoch || msg.Revision != seq+1 {
					t.Fatal("output order or epoch", msg)
				}
				seq = msg.Revision
				data, err := base64.StdEncoding.DecodeString(msg.Data.(string))
				if err != nil {
					t.Fatal(err)
				}
				text.Write(data)
				_ = m.Submit("phone", remote.Message{Type: "term_ack", ID: epoch, Pane: session, Revision: seq})
			}
		case <-deadline:
			t.Fatalf("echo missing %q", text.String())
		}
	}
	// A stale attachment cannot write into the shell.
	_ = m.Submit("phone", remote.Message{Type: "term_input", ID: "stale", Pane: session, Revision: 2, Data: base64.StdEncoding.EncodeToString([]byte("bad"))})
	refused := next(t, m, "term_result")
	if refused.Status != "refused" {
		t.Fatal(refused)
	}
}

func TestInputAndOutputLimits(t *testing.T) {
	a := &attachment{writes: make(chan input, 2), sizes: map[uint64]int{1: 3, 2: 4}, output: 2, outstanding: 7, credit: make(chan struct{}, 1)}
	msg := remote.Message{Revision: 1, Data: base64.StdEncoding.EncodeToString([]byte{0, 0xff, 0x1b, '['})}
	if err := a.queue(msg); err != nil {
		t.Fatal(err)
	}
	if err := a.queue(msg); err != nil {
		t.Fatal(err)
	}
	if len(a.writes) != 1 {
		t.Fatal("duplicate input queued twice")
	}
	in := <-a.writes
	if len(in.bytes) != 4 || in.bytes[1] != 0xff {
		t.Fatal("non-UTF8 input changed")
	}
	msg.Revision = 3
	if err := a.queue(msg); err == nil {
		t.Fatal("gap accepted")
	}
	if err := a.ack(3); err == nil {
		t.Fatal("future ACK accepted")
	}
	if err := a.ack(1); err != nil || a.outstanding != 4 {
		t.Fatal("bad credit", err)
	}
	if err := a.ack(1); err != nil || a.outstanding != 4 {
		t.Fatal("duplicate ACK added credit", err)
	}
	if err := a.ack(2); err != nil || a.outstanding != 0 {
		t.Fatal("cumulative ACK", err)
	}
}

func TestRevokedOwnerIsRefusedBeforeProcessWork(t *testing.T) {
	m := New(phoneterm.Registry{Root: t.TempDir(), Socket: "unused"}, nil, func(string) bool { return false })
	defer m.Close()
	if err := m.Submit("revoked", remote.Message{Type: "term_open", ID: "req", Data: map[string]int{"columns": 80, "rows": 24}}); err != nil {
		t.Fatal(err)
	}
	result := next(t, m, "term_result")
	if result.Status != "refused" || result.Reason != "Phone is no longer paired" {
		t.Fatal(result)
	}
}

func TestOutputFloodStopsAtAckWindow(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	registry := phoneterm.Registry{Root: t.TempDir(), Socket: fmt.Sprintf("ptln-test-flood-%d", os.Getpid())}
	defer exec.Command("tmux", "-L", registry.Socket, "kill-server").Run()
	m := New(registry, nil)
	defer m.Close()
	_ = m.Submit("phone", remote.Message{Type: "term_open", ID: "flood-open", Data: map[string]int{"columns": 80, "rows": 24}})
	opened := next(t, m, "term_result")
	if opened.Status != "ok" {
		t.Fatal(opened)
	}
	epoch := opened.Data.(map[string]any)["attachment"].(string)
	command := "yes '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdef'\r"
	_ = m.Submit("phone", remote.Message{Type: "term_input", ID: epoch, Pane: opened.Pane, Revision: 1, Data: base64.StdEncoding.EncodeToString([]byte(command))})
	deadline := time.After(20 * time.Second)
	total := 0
	var last uint64
	for total < Window-Chunk {
		select {
		case event := <-m.Events:
			if event.Message.Type == "term_output" {
				raw, _ := base64.StdEncoding.DecodeString(event.Message.Data.(string))
				total += len(raw)
				last = event.Message.Revision
				if total > Window {
					t.Fatal("output exceeded ACK window")
				}
			}
		case <-deadline:
			t.Fatalf("flood did not exercise window: %d bytes", total)
		}
	}
	time.Sleep(50 * time.Millisecond)
	for {
		select {
		case event := <-m.Events:
			if event.Message.Type == "term_output" {
				raw, _ := base64.StdEncoding.DecodeString(event.Message.Data.(string))
				total += len(raw)
				last = event.Message.Revision
			}
		default:
			goto drained
		}
	}
drained:
	if total > Window {
		t.Fatalf("unacknowledged flood is unbounded: %d", total)
	}
	_ = m.Submit("phone", remote.Message{Type: "term_ack", ID: epoch, Pane: opened.Pane, Revision: last})
	output := next(t, m, "term_output")
	if output.Revision != last+1 {
		t.Fatal("ACK did not resume ordered output", output.Revision, last)
	}
}

func TestAuditRefusalPreventsShellCreation(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	registry := phoneterm.Registry{Root: t.TempDir(), Socket: fmt.Sprintf("ptln-test-no-audit-%d", os.Getpid())}
	defer exec.Command("tmux", "-L", registry.Socket, "kill-server").Run()
	m := New(registry, func(string, string, string) error { return fmt.Errorf("audit unavailable") })
	defer m.Close()
	_ = m.Submit("phone", remote.Message{Type: "term_open", ID: "unaudited", Data: map[string]int{"columns": 80, "rows": 24}})
	result := next(t, m, "term_result")
	if result.Status != "refused" {
		t.Fatal(result)
	}
	rows, err := registry.List("")
	if err != nil || len(rows) != 0 {
		t.Fatal("unaudited shell persisted", rows, err)
	}
	if err = exec.Command("tmux", "-L", registry.Socket, "list-sessions").Run(); err == nil {
		t.Fatal("unaudited shell started")
	}
}
