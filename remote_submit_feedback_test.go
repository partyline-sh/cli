package main

import (
	"encoding/json"
	"os"
	"partyline.sh/partyline/internal/remote"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRemoteDeliveryClassification(t *testing.T) {
	for _, tt := range []struct{ screen, text, phase string }{
		{"› hello\n\n──────────────────\ngpt-6 · 98% context left", "hello", "in_composer"},
		{"› hello", "hello", "in_composer"},
		{"› different", "hello", "delivered"},
		{"old hello\n› different", "hello", "delivered"},
		{"› hello\nadditional draft", "hello", "delivered"},
		{"› [Pasted text #1 +8 lines]", "hello\nworld", "delivered"},
		{"$ hello\n$", "hello", "delivered"},
		{"Working (1s)\n›", "hello", "agent_working"},
	} {
		phase, fingerprint := remoteDeliveryState(tt.screen, tt.text, "")
		if phase != tt.phase {
			t.Fatalf("%q: %s", tt.screen, phase)
		}
		if (fingerprint != "") != (phase == "in_composer") {
			t.Fatal("unsafe retry offered", tt.screen)
		}
	}
}

func waitInputResult(t *testing.T, s *remoteInputStore, route, kind string) remote.Message {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for i, m := range s.mail[route] {
			if m.Type == kind {
				s.mail[route] = append(s.mail[route][:i], s.mail[route][i+1:]...)
				s.mu.Unlock()
				return m
			}
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no asynchronous result", kind)
	return remote.Message{}
}

func holdRemoteSubmit(t *testing.T, s *remoteInputStore) (<-chan struct{}, func()) {
	t.Helper()
	oldWait := remoteSubmitWait
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	remoteSubmitWait = func(d time.Duration) {
		if d == 300*time.Millisecond {
			entered <- struct{}{}
			<-release
		}
	}
	t.Cleanup(func() { finish(); s.workers.Wait(); remoteSubmitWait = oldWait })
	return entered, finish
}

func TestRemoteSubmitDoesNotHoldInputLock(t *testing.T) {
	tmuxTestServer(t, "ptln-test-submit-async")
	s, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	oldSnapshot := remoteSubmitSnapshot
	t.Cleanup(func() { remoteSubmitSnapshot = oldSnapshot })
	entered, release := holdRemoteSubmit(t, s)
	remoteSubmitSnapshot = func(string) (string, error) { return "Working (1s)\n›", nil }
	m := remote.Message{Type: "input", ID: "async", Pane: pane, Kind: "text", Data: "hello"}
	if r := s.handleAsync(route, m, time.Now()); r.Status != "pending" {
		t.Fatal(r)
	}
	<-entered
	duplicate := make(chan remote.Message, 1)
	go func() { duplicate <- s.handleAsync(route, m, time.Now()) }()
	select {
	case r := <-duplicate:
		if r.Status != "pending" {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		release()
		t.Fatal("input lock held across submit delay")
	}
	other := remote.Message{Type: "input", ID: "interleaved", Pane: pane, Kind: "key", Data: "Enter"}
	if r := s.handleAsync(route, other, time.Now()); r.Status != "refused" {
		t.Fatal("pending draft was interleaved", r)
	}
	release()
	if r := waitInputResult(t, s, route, "input_result"); r.Status != "ok" {
		t.Fatal(r)
	}
	waitRemoteBytes(t, read, []byte("hello\r"))
	result := waitInputResult(t, s, route, "input_delivery")
	if result.Data.(map[string]any)["inputDelivery"] != "agent_working" {
		t.Fatal(result)
	}
	if r := s.handleAsync(route, m, time.Now()); r.Status != "ok" {
		t.Fatal(r)
	}
	waitRemoteBytes(t, read, []byte("hello\r"))
}

func TestRetryEnterRequiresUnchangedOriginalDraft(t *testing.T) {
	tmuxTestServer(t, "ptln-test-submit-retry")
	s, route := remoteInputFixture(t)
	pane, read := remoteInputRecorder(t, true)
	oldWait, oldSnapshot := remoteSubmitWait, remoteSubmitSnapshot
	remoteSubmitWait = func(time.Duration) {}
	screen := "› hello"
	remoteSubmitSnapshot = func(string) (string, error) { return screen, nil }
	t.Cleanup(func() { s.workers.Wait(); remoteSubmitWait = oldWait; remoteSubmitSnapshot = oldSnapshot })
	m := remote.Message{Type: "input", ID: "first", Pane: pane, Kind: "text", Data: "hello"}
	if r := s.handleAsync(route, m, time.Now()); r.Status != "pending" {
		t.Fatal(r)
	}
	feedback := waitInputResult(t, s, route, "input_delivery")
	if feedback.Data.(map[string]any)["retryInputId"] != "first" {
		t.Fatal(feedback)
	}
	wrongPane := remote.Message{Type: "input_submit", ID: "wrong-pane", Pane: "%99999", Data: "first"}
	if r := s.handleAsync(route, wrongPane, time.Now()); r.Status != "refused" {
		t.Fatal("retry crossed panes", r)
	}
	s.mu.Lock()
	target := s.phones[route].submits["first"]
	expired := target
	expired.until = time.Now().Add(-time.Second)
	s.phones[route].submits["first"] = expired
	s.mu.Unlock()
	if r := s.handleAsync(route, remote.Message{Type: "input_submit", ID: "expired", Pane: pane, Data: "first"}, time.Now()); r.Status != "refused" {
		t.Fatal("expired retry accepted", r)
	}
	s.mu.Lock()
	s.phones[route].submits["first"] = target
	s.mu.Unlock()
	phones, _ := remotePhones()
	otherRoute := "abcdefabcdefabcdefabcdefabcdefab"
	phones = append(phones, remotePhone{ID: otherRoute})
	wire, _ := json.Marshal(phones)
	if err := os.WriteFile(filepath.Join(relayDir(), "phones.json"), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if r := s.handleAsync(otherRoute, remote.Message{Type: "input_submit", ID: "cross-phone", Pane: pane, Data: "first"}, time.Now()); r.Status != "refused" {
		t.Fatal("retry crossed phones", r)
	}
	screen = "› changed"
	retry := remote.Message{Type: "input_submit", ID: "retry-changed", Pane: pane, Data: "first"}
	if r := s.handleAsync(route, retry, time.Now()); r.Status != "refused" || !strings.Contains(r.Reason, "draft changed") {
		t.Fatal(r)
	}
	screen = "› hello"
	retry.ID = "retry-valid"
	if r := s.handleAsync(route, retry, time.Now()); r.Status != "pending" {
		t.Fatal(r)
	}
	waitInputResult(t, s, route, "input_delivery")
	waitRemoteBytes(t, read, []byte("hello\r\r"))
	retry.ID = "stale-button"
	if r := s.handleAsync(route, retry, time.Now()); r.Status != "refused" {
		t.Fatal("original retry was reusable", r)
	}
}
