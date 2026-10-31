package main

import (
	"strings"
	"testing"
	"time"
)

// A command run inside a session finds its pane from its own ancestry, not from $TMUX_PANE: codex
// replays an old shell snapshot whose TMUX_PANE names a pane that no longer exists.
func TestChatSelfPaneFromAncestry(t *testing.T) {
	t.Setenv("TMUX_PANE", "%15") // the stale value codex replayed
	t.Setenv("PARTYLINE_SESSION_KEY", "")
	t.Setenv("CODEX_THREAD_ID", "")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "%17" }
	if keys := chatSelfKeys(""); len(keys) != 1 || keys[0] != "pane:%17" {
		t.Errorf("keys %v, want the ancestry's pane:%%17", keys)
	}
	chatAncestorPane = func() string { return "" } // tmux cannot be asked: the variable is the fallback
	if keys := chatSelfKeys(""); len(keys) != 1 || keys[0] != "pane:%15" {
		t.Errorf("fallback keys %v", keys)
	}
	if p := paneOfAncestry([]int{900, 74963, 1}, map[int]string{74963: "%17", 5: "%3"}); p != "%17" {
		t.Errorf("paneOfAncestry = %q", p)
	}
	if p := paneOfAncestry([]int{900}, map[int]string{74963: "%17"}); p != "" {
		t.Errorf("a process under no pane found %q", p)
	}
}

// A member woken chatWakeMax times whose read mark never moves is not woken again, and the room
// says why; once it reads, wakes start again.
func TestChatWakeStopsWithoutReads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldPanes, oldWake, oldTries := chatPanesNow, wakePaneNow, chatWakeTries
	t.Cleanup(func() { chatPanesNow, wakePaneNow, chatWakeTries = oldPanes, oldWake, oldTries })
	chatWakeTries = map[string]chatTries{}
	chatPanesNow = func() []chatPane { return []chatPane{{ID: "%17", Key: "pane:%17", Engine: "codex", Window: 11}} }
	wakes := 0
	wakePaneNow = func(string, string) error { wakes++; return nil }
	_ = withChatLock(func(r *chatRoom) error {
		r.Members = append(r.Members, chatMember{Key: "pane:%17", Name: "#11 codex", Engine: "codex", Active: time.Now().Add(-time.Hour)})
		_, err := postLocked(r, chatMessage{From: "pane:%16", Name: "#10 claude", Text: "hello"})
		return err
	})
	var why string
	setWake := func(key, w string, drop bool) { why = w }
	for i := 0; i < chatWakeMax+3; i++ {
		chatWakeIdle(map[string]time.Time{}, setWake) // a fresh lastTry: no 20 s spacing in the test
	}
	if wakes != chatWakeMax || !strings.Contains(why, "without reading") {
		t.Fatalf("%d wakes (want %d), status %q", wakes, chatWakeMax, why)
	}
	// It reads; a new message wakes it again.
	_ = withChatLock(func(r *chatRoom) error {
		msgs := readChat()
		r.member("pane:%17").Read = msgs[len(msgs)-1].Seq
		_, err := postLocked(r, chatMessage{From: "pane:%16", Name: "#10 claude", Text: "again"})
		return err
	})
	chatWakeIdle(map[string]time.Time{}, setWake)
	if wakes != chatWakeMax+1 || why != "" {
		t.Errorf("after reading: %d wakes, status %q", wakes, why)
	}
}

// Codex strips PARTYLINE_SESSION_KEY (its name contains KEY) from the commands it runs; its session
// id arrives as CODEX_THREAD_ID, and the room, which knows the member by that id, finds it.
func TestChatSelfKeysCodexThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PARTYLINE_SESSION_KEY", "")
	t.Setenv("CODEX_THREAD_ID", "01a14d24-38d1-7d62-abcd-db8856de86c1")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "%21" }
	r := &chatRoom{Members: []chatMember{{Key: "01a14d24-38d1-7d62-abcd-db8856de86c1", Name: "#13 codex", Engine: "codex"}}}
	if m := r.self(chatSelfKeys("")); m == nil || m.Name != "#13 codex" {
		t.Fatalf("codex session not found by its thread id: keys %v", chatSelfKeys(""))
	}
}
