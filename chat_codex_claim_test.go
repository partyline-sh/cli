package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// claimStubs replaces the codex-thread lookup, the pane list and the pane retag for one test.
func claimStubs(t *testing.T, threads []codexThread, panes []chatPane) *map[string]string {
	t.Helper()
	oldThreads, oldPanes, oldRetag := chatCodexThreads, chatPanesNow, chatRetagPane
	t.Cleanup(func() { chatCodexThreads, chatPanesNow, chatRetagPane = oldThreads, oldPanes, oldRetag })
	chatCodexThreads = func(time.Time) []codexThread { return append([]codexThread(nil), threads...) }
	chatPanesNow = func() []chatPane { return panes }
	retagged := map[string]string{}
	chatRetagPane = func(pane, key string) error { retagged[pane] = key; return nil }
	return &retagged
}

func placeholder(at time.Time) string { return fmt.Sprintf("new-codex-%d", at.UnixNano()) }

// A codex command carries its own CODEX_THREAD_ID and a stale TMUX_PANE (codex replays an old shell
// snapshot, and its commands run under the codex daemon, outside tmux). The stale pane must never
// let it read or send as the member who owns that pane.
func TestCodexStalePaneCannotImpersonateAnotherMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PARTYLINE_SESSION_KEY", "")
	t.Setenv("CODEX_THREAD_ID", "this-is-a-different-thread")
	t.Setenv("TMUX_PANE", "%15")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "" } // under the codex daemon: no pane ancestor
	claimStubs(t, nil, nil)

	r := &chatRoom{Members: []chatMember{{Key: "pane:%15", Name: "#99 codex", Engine: "codex"}}}
	if m := chatSelfOrClaim(r, chatSelfKeys("")); m != nil {
		t.Fatalf("a stale TMUX_PANE made this codex %s (keys %v)", m.Name, chatSelfKeys(""))
	}
}

// Without a codex thread (a plain shell, or Claude) the TMUX_PANE fallback still applies.
func TestTmuxPaneFallbackStillWorksOutsideCodex(t *testing.T) {
	t.Setenv("PARTYLINE_SESSION_KEY", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("TMUX_PANE", "%15")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "" }
	if keys := chatSelfKeys(""); len(keys) != 1 || keys[0] != "pane:%15" {
		t.Errorf("keys %v, want the pane fallback", keys)
	}
}

// A codex started by `ptln new` is in the room under its launch placeholder, which codex cannot
// present. Its first read claims the placeholder: the member and the pane both move to the thread
// id, so reads and wakes agree from then on, and its unread mark is kept.
func TestNewCodexClaimsItsPlaceholderOnFirstRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "01a22033-d7d8-7522-b4d3-a33f4f224499")
	t.Setenv("TMUX_PANE", "")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "" }

	launch := time.Now().Add(-time.Minute)
	key := placeholder(launch)
	retagged := claimStubs(t,
		[]codexThread{{ID: "01a22033-d7d8-7522-b4d3-a33f4f224499", Cwd: "/p", Started: launch.Add(2 * time.Second)}},
		[]chatPane{{ID: "%20", Key: key, Engine: "codex", Window: 11, Cwd: "/p"}})
	r := &chatRoom{Members: []chatMember{{Key: key, Name: "#11 codex", Engine: "codex", Read: 2903}}}

	m := chatSelfOrClaim(r, chatSelfKeys(""))
	if m == nil || m.Name != "#11 codex" {
		t.Fatalf("the new codex did not claim its placeholder")
	}
	if m.Key != "01a22033-d7d8-7522-b4d3-a33f4f224499" || m.Read != 2903 {
		t.Errorf("claimed member is %+v; want the thread id as key and the read mark kept", m)
	}
	if (*retagged)["%20"] != "01a22033-d7d8-7522-b4d3-a33f4f224499" {
		t.Errorf("the pane was not retagged to the thread id, so wakes and reads would disagree: %v", *retagged)
	}
	// From then on it is found directly, with no second claim.
	if again := r.self(chatSelfKeys("")); again == nil || again.Name != "#11 codex" {
		t.Error("after claiming, the member is not found by its thread id")
	}
}

// Several new codex sessions: each placeholder takes the thread its own pane started, in launch
// order, within the claim window, and never one from another folder or one that started long after.
func TestCodexClaimPairsEachPlaceholderWithItsOwnThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "thread-b")
	t.Setenv("TMUX_PANE", "")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "" }

	base := time.Now().Add(-time.Hour)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	p1, p2, p3, p4 := placeholder(at(0)), placeholder(at(30)), placeholder(at(300)), placeholder(at(40))
	claimStubs(t, []codexThread{
		{ID: "thread-a", Cwd: "/p", Started: at(1)},
		{ID: "thread-b", Cwd: "/p", Started: at(31)},
		{ID: "thread-c", Cwd: "/q", Started: at(41)},
	}, []chatPane{
		{ID: "%1", Key: p1, Engine: "codex", Cwd: "/p"},
		{ID: "%2", Key: p2, Engine: "codex", Cwd: "/p"},
		{ID: "%3", Key: p3, Engine: "codex", Cwd: "/p"},
		{ID: "%4", Key: p4, Engine: "codex", Cwd: "/q"},
	})
	r := &chatRoom{Members: []chatMember{
		{Key: p1, Name: "#1 codex", Engine: "codex"},
		{Key: p2, Name: "#2 codex", Engine: "codex"},
		{Key: p3, Name: "#3 codex", Engine: "codex"},
		{Key: p4, Name: "#4 codex", Engine: "codex"},
	}}
	if m := chatSelfOrClaim(r, chatSelfKeys("")); m == nil || m.Name != "#2 codex" {
		t.Fatalf("claimed %+v, want #2 codex", m)
	}
	want := map[string]string{"#1 codex": "thread-a", "#2 codex": "thread-b", "#3 codex": p3, "#4 codex": "thread-c"}
	for _, m := range r.Members {
		if m.Key != want[m.Name] {
			t.Errorf("%s has key %s, want %s", m.Name, m.Key, want[m.Name])
		}
	}
}

// A sandboxed codex cannot reach tmux, so its own read cannot claim. The waker (outside the sandbox)
// claims on its pass, and the next read finds the member by thread id alone.
func TestWakerClaimsForASandboxedCodex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	launch := time.Now().Add(-time.Minute)
	key := placeholder(launch)
	retagged := claimStubs(t,
		[]codexThread{{ID: "thread-s", Cwd: "/p", Started: launch.Add(time.Second)}},
		[]chatPane{{ID: "%30", Key: key, Engine: "codex", Window: 12, Cwd: "/p"}})
	_ = withChatLock(func(r *chatRoom) error {
		r.Members = append(r.Members, chatMember{Key: key, Name: "#12 codex", Engine: "codex"})
		return nil
	})
	chatClaimCodexPlaceholders()
	var found *chatMember
	_ = withChatLock(func(r *chatRoom) error {
		if m := r.member("thread-s"); m != nil {
			cp := *m
			found = &cp
		}
		return nil
	})
	if found == nil || found.Name != "#12 codex" || (*retagged)["%30"] != "thread-s" {
		t.Fatalf("waker did not claim: member %+v, retagged %v", found, *retagged)
	}
	// Inside the sandbox: thread id only, no tmux.
	t.Setenv("CODEX_THREAD_ID", "thread-s")
	t.Setenv("TMUX_PANE", "%99")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "" }
	chatPanesNow = func() []chatPane { return nil }
	_ = withChatLock(func(r *chatRoom) error {
		if m := chatSelfOrClaim(r, chatSelfKeys("")); m == nil || m.Name != "#12 codex" {
			t.Errorf("the sandboxed read did not find its member: %+v", m)
		}
		return nil
	})
}

// No placeholder in this folder: nothing is claimed, and the read reports "not in the chat".
func TestCodexClaimNeedsAMatchingPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "thread-x")
	t.Setenv("TMUX_PANE", "")
	old := chatAncestorPane
	t.Cleanup(func() { chatAncestorPane = old })
	chatAncestorPane = func() string { return "" }
	launch := time.Now().Add(-time.Minute)
	key := placeholder(launch)
	claimStubs(t, []codexThread{{ID: "thread-x", Cwd: "/p", Started: launch.Add(time.Second)}}, nil) // its tab is gone
	r := &chatRoom{Members: []chatMember{{Key: key, Name: "#5 codex", Engine: "codex"}}}
	if m := chatSelfOrClaim(r, chatSelfKeys("")); m != nil {
		t.Fatalf("claimed %s with no pane to retag", m.Name)
	}
}

// The thread list reads each rollout's session_meta: id, folder and start time.
func TestCodexThreadsSinceReadsTheRollouts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "11", "27")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "01a22033-d7d8-7522-b4d3-a33f4f224499"
	line := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"/Users/x/dev/compound","timestamp":"2026-11-28T03:29:58.123Z"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-27T20-29-58-"+id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-bad.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	since, _ := time.Parse(time.RFC3339, "2026-11-28T03:29:00Z")
	got := codexThreadsSince(since)
	if len(got) != 1 || got[0].ID != id || got[0].Cwd != "/Users/x/dev/compound" || got[0].Started.UTC().Format(time.RFC3339) != "2026-11-28T03:29:58Z" {
		t.Errorf("codexThreadsSince = %+v", got)
	}
	later, _ := time.Parse(time.RFC3339, "2026-11-28T04:00:00Z")
	if got := codexThreadsSince(later); len(got) != 0 {
		t.Errorf("a thread from before %v was listed: %+v", later, got)
	}
}

// Every codex launch can write the chat directory, and only that; other engines are untouched.
func TestCodexLaunchesCanWriteTheChatDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := chatDir()
	got := withCodexChatDir("codex", []string{"codex", "resume", "abc"})
	want := []string{"codex", "resume", "abc", "--add-dir", dir}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if again := withCodexChatDir("codex", got); len(again) != len(got) {
		t.Errorf("not idempotent: %v", again)
	}
	if c := withCodexChatDir("claude", []string{"claude"}); len(c) != 1 {
		t.Errorf("claude argv changed: %v", c)
	}
	if spec := prepareLaunchSpec(Spec{Argv: []string{"codex"}}); fmt.Sprint(spec.Argv) != fmt.Sprint([]string{"codex", "--add-dir", dir}) {
		t.Errorf("prepareLaunchSpec did not add the chat dir: %v", spec.Argv)
	}
}

// A message addressed to the codex under its launch placeholder, before the claim, is still
// delivered once the member has moved to its thread id.
func TestAMessageToThePlaceholderArrivesAfterTheClaim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	launch := time.Now().Add(-time.Minute)
	key := placeholder(launch)
	claimStubs(t,
		[]codexThread{{ID: "thread-m", Cwd: "/p", Started: launch.Add(time.Second)}},
		[]chatPane{{ID: "%40", Key: key, Engine: "codex", Window: 13, Cwd: "/p"}})
	_ = withChatLock(func(r *chatRoom) error {
		r.Members = append(r.Members, chatMember{Key: key, Name: "#13 codex", Engine: "codex"})
		_, err := postLocked(r, chatMessage{From: "you", Name: "you", To: []string{key}, Text: "just for you"})
		return err
	})
	chatClaimCodexPlaceholders()
	_ = withChatLock(func(r *chatRoom) error {
		m := r.member("thread-m")
		if m == nil {
			t.Fatal("not claimed")
		}
		got := unreadFor(readChat(), *m)
		if len(got) != 1 || got[0].Text != "just for you" {
			t.Errorf("the targeted message was lost by the claim: %+v", got)
		}
		return nil
	})
}

// The pane's stored Spec follows the claim: keyed by the thread and relaunched as a resume.
func TestClaimedCodexSpecResumesTheThread(t *testing.T) {
	sp := claimedCodexSpec(Spec{Key: "new-codex-1", Argv: []string{"codex", "--add-dir", "/c"}}, "thread-z")
	if sp.Key != "thread-z" || fmt.Sprint(sp.Argv) != fmt.Sprint([]string{"codex", "resume", "thread-z", "--add-dir", "/c"}) {
		t.Errorf("spec %+v", sp)
	}
	if again := claimedCodexSpec(sp, "thread-z"); fmt.Sprint(again.Argv) != fmt.Sprint(sp.Argv) {
		t.Errorf("not idempotent: %v", again.Argv)
	}
}
