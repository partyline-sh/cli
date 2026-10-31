package main

import (
	"strings"
	"testing"
	"time"
)

func chatTestRoom(t *testing.T, members ...chatMember) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := withChatLock(func(r *chatRoom) error { r.Members = members; return nil }); err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, m chatMessage) {
	t.Helper()
	if err := withChatLock(func(r *chatRoom) error { _, err := postLocked(r, m); return err }); err != nil {
		t.Fatal(err)
	}
}

func stop(t *testing.T, key string, in chatStopInput) string {
	t.Helper()
	reason := ""
	if err := withChatLock(func(r *chatRoom) error {
		var err error
		reason, err = chatStopDecide(r, []string{key}, in, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return reason
}

var listening = chatStopInput{BackgroundTasks: []struct {
	Status  string `json:"status"`
	Command string `json:"command"`
}{{Status: "running", Command: "ptln chat listen"}}}

// A message for a member is handed to it when its turn ends, with how to reply. The session's own
// final text is never posted for it: when it has nothing to add that text is narration, and posting
// it set two sessions narrating at each other in the first end-to-end run.
func TestChatStopHookDeliversAndPostsNothingItself(t *testing.T) {
	chatTestRoom(t, chatMember{Key: "a", Name: "#11 claude", Engine: "claude"}, chatMember{Key: "b", Name: "#12 codex", Engine: "codex"})
	post(t, chatMessage{From: "you", Name: "you", Text: "which runtime should the pilot use?"})

	reason := stop(t, "a", listening)
	if !strings.Contains(reason, "which runtime should the pilot use?") || !strings.Contains(reason, "ptln chat send") {
		t.Fatalf("the waiting message and how to reply should be handed over:\n%s", reason)
	}
	before := len(readChat())
	if again := stop(t, "a", listening); again != "" {
		t.Errorf("nothing else is waiting, so the session should stop: %q", again)
	}
	if len(readChat()) != before {
		t.Error("the Stop hook must not post anything on the session's behalf")
	}
	t.Setenv("PARTYLINE_SESSION_KEY", "a")
	chatSendSelf("Node 24 on Render; @12 do you agree?")
	msgs := readChat()
	last := msgs[len(msgs)-1]
	if last.From != "a" || len(last.To) != 1 || last.To[0] != "b" {
		t.Errorf("the sent reply should come from #11 and @12 should address only #12: %+v", last)
	}
}

// Agents answering each other is the point, but not unattended for ever.
func TestChatPausesAgentsUntilThePersonSpeaks(t *testing.T) {
	chatTestRoom(t, chatMember{Key: "a", Name: "#11 claude", Engine: "claude"}, chatMember{Key: "b", Name: "#12 codex", Engine: "codex"})
	for i := 0; i < chatAgentRunCap; i++ {
		post(t, chatMessage{From: "b", Name: "#12 codex", Text: "another point"})
	}
	if got := stop(t, "a", listening); got != "" {
		t.Fatalf("after %d agent messages in a row nothing more should be delivered: %q", chatAgentRunCap, got)
	}
	post(t, chatMessage{From: "you", Name: "you", Text: "carry on"})
	if got := stop(t, "a", listening); !strings.Contains(got, "carry on") {
		t.Fatal("the person speaking should resume delivery")
	}
}

// An idle Claude member with no listener is asked to start one, and only once in a while.
func TestChatNudgesAClaudeMemberWithNoListener(t *testing.T) {
	chatTestRoom(t, chatMember{Key: "a", Name: "#11 claude", Engine: "claude"})
	if got := stop(t, "a", chatStopInput{}); got != chatListenNudge {
		t.Fatalf("expected the listener nudge, got %q", got)
	}
	if got := stop(t, "a", chatStopInput{}); got != "" {
		t.Error("the nudge must not repeat on every stop")
	}
	if got := stop(t, "nobody", chatStopInput{}); got != "" {
		t.Error("a session that is not in the room must be left alone")
	}
}

func TestChatAddressing(t *testing.T) {
	r := &chatRoom{Members: []chatMember{{Key: "a", Name: "#11 claude", Engine: "claude"}, {Key: "b", Name: "#12.2 codex", Engine: "codex"}}}
	cases := map[string][]string{
		"hello everyone":         nil,
		"@11 what do you think?": {"a"},
		"@codex, and you?":       {"b"},
		"@12.2 check":            {"b"},
		"@all listen":            nil,
	}
	for text, want := range cases {
		got := chatAddressees(r, text)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v want %v", text, got, want)
		}
	}
	msgs := []chatMessage{{Seq: 1, From: "you", Text: "all"}, {Seq: 2, From: "you", To: []string{"b"}, Text: "only b"}, {Seq: 3, From: "a", Text: "mine"}, {Seq: 4, Note: true, Text: "joined"}}
	if got := unreadFor(msgs, r.Members[0]); len(got) != 1 || got[0].Seq != 1 {
		t.Errorf("a should get only the room-wide message, not b's, its own, or notes: %+v", got)
	}
}

func TestChatPersonSendWithLiteralAtSigns(t *testing.T) {
	for _, tc := range []struct {
		text string
		to   []string
	}{
		{"@", nil},
		{"Please explain the @ symbol.", nil},
		{"The symbol is @)", nil},
		{"The symbol is @\"", nil},
		{"@codex please explain @", []string{"b"}},
		{"Explain @ to @11", []string{"a"}},
		{"@11 @, @codex check this", []string{"a", "b"}},
		{"@codex the symbol is @)", []string{"b"}},
		{"@codex the symbol is @\"", []string{"b"}},
		{"@all @codex please explain @", nil},
		{"@codex please explain @ to @all", nil},
		{"@99 this is explicitly for @all", nil},
		{"@11) please check", []string{"a"}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			chatTestRoom(t, chatMember{Key: "a", Name: "#11 claude", Engine: "claude"}, chatMember{Key: "b", Name: "#12 codex", Engine: "codex"})
			if err := chatSendPerson(tc.text); err != nil {
				t.Fatalf("ordinary at-sign text must not block sending: %v", err)
			}
			messages := readChat()
			if len(messages) != 1 || messages[0].Text != tc.text || strings.Join(messages[0].To, ",") != strings.Join(tc.to, ",") {
				t.Fatalf("sent message or recipients changed: %+v, want %q to %v", messages, tc.text, tc.to)
			}
		})
	}

	t.Run("an actual unknown recipient still refuses the send", func(t *testing.T) {
		chatTestRoom(t, chatMember{Key: "a", Name: "#11 claude", Engine: "claude"})
		if err := chatSendPerson("@99 please explain @"); err == nil || !strings.Contains(err.Error(), "not sent") {
			t.Fatalf("unknown recipient must not broadcast: %v", err)
		}
		if len(readChat()) != 0 {
			t.Fatal("message for an unknown recipient was posted")
		}
	})
}

// The screen test, against the bottom of real Claude 2.1.310 and codex 0.163.0 panes, with their
// colours: both draw hint text dim, and that is what separates a hint from typed text.
func TestChatScreenIdleEmpty(t *testing.T) {
	claudeEmpty := "──────────\n\x1b[39m❯ \n──────────\n  xero-receipts (main) Opus 5.5\n  ⏵⏵ bypass permissions on"
	claudeSuggestion := "──────────\n\x1b[39m❯ \x1b[2mjoin the partyline chat\x1b[0m\n──────────\n  assetmgmt (main) Haiku 5.5"
	claudeTyped := "──────────\n\x1b[39m❯ abc\n──────────\n  assetmgmt (main) Haiku 5.5"
	codexEmpty := "  1 background terminal running\n\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n  GPT-6.1-Sol low · ~/dev/cyberpunk-game"
	codexTyped := "  1 background terminal running\n\x1b[1m›\x1b[0m fix the build\n  GPT-6.1-Sol low"
	cases := []struct {
		name, screen string
		ok           bool
	}{
		{"claude empty", claudeEmpty, true},
		{"claude showing a dim suggested prompt", claudeSuggestion, true},
		{"claude with text typed", claudeTyped, false},
		{"codex with its dim placeholder", codexEmpty, true},
		{"codex with text typed", codexTyped, false},
		{"claude working", "✻ Brewing… (\x1b[2mesc to interrupt\x1b[0m)\n\x1b[39m❯ ", false},
		{"no input box", "$ ls\nfoo bar", false},
	}
	for _, c := range cases {
		if ok, why := screenIdleEmpty(c.screen); ok != c.ok {
			t.Errorf("%s: got %v (%s), want %v", c.name, ok, why, c.ok)
		}
	}
}

// A session can be known to the room by its pane and to itself by its partyline key, or the
// other way round; either must find it.
func TestChatFindsAMemberByAnyIdentity(t *testing.T) {
	r := &chatRoom{Members: []chatMember{{Key: "pane:%4", Name: "#4 claude"}}}
	if r.self([]string{"K1", "pane:%4", "sess"}) == nil {
		t.Error("a member known by its pane should be found from a process with a partyline key")
	}
	if r.self([]string{"K1"}) != nil {
		t.Error("an unrelated key must not match")
	}
}

// A long message is wrapped under its header instead of running off as one line, list items keep
// a hanging indent, and nothing is wider than the terminal.
func TestChatFormatWrapsUnderAHeader(t *testing.T) {
	m := chatMessage{From: "a", Name: "#10 claude", At: time.Date(2026, 10, 24, 21, 2, 0, 0, time.Local),
		Text: "We agree on the product. The open choice is software with bounded operations or a managed service, and the pilot has to measure onboarding effort.\n- walkthrough with your contact\n- **Helix** demo of the same five steps"}
	out := stripColours(chatFormat(m, 50))
	lines := strings.Split(out, "\n")
	if lines[0] != "#10 claude · 21:02" {
		t.Fatalf("first line should be the header, got %q", lines[0])
	}
	for _, l := range lines {
		if runewidthOf(l) > 50 {
			t.Errorf("line wider than the terminal: %q", l)
		}
	}
	if !strings.Contains(out, "\n  • walkthrough with your contact") || !strings.Contains(out, "  • Helix demo") {
		t.Errorf("list items should be bulleted and indented:\n%s", out)
	}
	if strings.Contains(out, "**") {
		t.Error("**bold** markers should be rendered, not shown")
	}
	for _, l := range lines[1:] {
		if l != "" && !strings.HasPrefix(l, "  ") {
			t.Errorf("body lines should be indented under the header: %q", l)
		}
	}
}

func runewidthOf(s string) int { return len([]rune(s)) }

// Markdown is rendered, not shown as markup, and nothing is wider than the terminal.
func TestChatMarkdownRenders(t *testing.T) {
	src := "## Recap\n\nWe agree on **security maintenance and emergency containment for apps that business teams build** only.\n\n1. walkthrough\n2. `Helix` demo\n   - same five steps\n\n> a guess\n\n| item | owner |\n|---|---|\n| pilot runtime | you |\n\nSee [the doc](https://example.com)."
	out := renderMarkdown(src, "  ", 50)
	plain := stripColours(out)
	for _, markup := range []string{"##", "**", "`", "](", "|---"} {
		if strings.Contains(plain, markup) {
			t.Errorf("markup %q should be rendered, not shown:\n%s", markup, plain)
		}
	}
	for _, want := range []string{"  Recap", "  1. walkthrough", "  2. Helix demo", "     • same five steps", "  │ a guess", "the doc https://example.com", "pilot runtime"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	for _, l := range strings.Split(plain, "\n") {
		if runewidthOf(l) > 50 {
			t.Errorf("line wider than 50: %q", l)
		}
	}
	// The bold phrase wraps onto a second line and must still be bold there.
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(stripColours(l), "build") && !strings.Contains(l, sgrBold) {
			t.Errorf("bold did not carry across the line break: %q", l)
		}
	}
}

// Each session's status after a message: delivered and working shows a verb and the time taken;
// waiting shows why; a reply shows briefly; an idle session shows nothing.
func TestChatStatusLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	r := &chatRoom{Members: []chatMember{
		{Key: "a", Name: "#10 claude", Read: 3, Answering: true, Delivered: now.Add(-12 * time.Second)},
		{Key: "b", Name: "#11 codex", Read: 1},
		{Key: "c", Name: "#12 claude", Read: 3, Sent: true, Active: now.Add(-time.Second)},
		{Key: "d", Name: "#13 claude", Read: 3},
	}}
	msgs := []chatMessage{{Seq: 1}, {Seq: 2, From: "you", Text: "?"}, {Seq: 3, From: "a", Text: "hi"}}
	lines, spinning := chatStatusLines(r, msgs, map[string]string{"b": "you are looking at it"}, nil, now)
	joined := strings.Join(lines, "\n")
	if !spinning || !strings.Contains(joined, "#10 claude · "+chatVerb("a", 3)+"… 12s") {
		t.Errorf("a working session should show a verb and how long it has taken:\n%s", joined)
	}
	if !strings.Contains(joined, "#11 codex · idle, 2 messages waiting: you are looking at it") {
		t.Errorf("a held-back wake should say why:\n%s", joined)
	}
	if !strings.Contains(joined, "✓ #12 claude · replied") {
		t.Errorf("a fresh reply should show:\n%s", joined)
	}
	if strings.Contains(joined, "#13") {
		t.Errorf("an idle session with nothing waiting should not show:\n%s", joined)
	}
	if chatVerb("a", 3) != chatVerb("a", 3) || chatVerb("a", 3) == chatVerb("a", 4) && chatVerb("a", 4) == chatVerb("a", 5) {
		t.Error("the verb should hold for a turn and vary between turns")
	}
}

// Review fixes: colour values are not dim, a highlighted menu choice is not an input box, a paste
// end split across reads still ends the paste, control characters never reach the terminal, and
// an @mention that matches nobody is not sent to everyone.
func TestChatReviewFixes(t *testing.T) {
	if ok, _ := screenIdleEmpty("\x1b[39m❯ \x1b[38;2;255;255;255mhello"); ok {
		t.Error("truecolor typed text was read as dim hint text")
	}
	if ok, _ := screenIdleEmpty("\x1b[39m❯ \x1b[38;5;2mhello"); ok {
		t.Error("256-colour typed text was read as dim hint text")
	}
	if ok, why := screenIdleEmpty("Do you want to proceed?\n❯ 1. Yes\n  2. No"); ok || why != "it is waiting for your approval" {
		t.Errorf("a highlighted menu choice must not count as an empty box, got %v %q", ok, why)
	}
	in := newChatInput(nil)
	in.handle([]byte(pasteStart + "hello\x1b[2"))
	in.handle([]byte("01~"))
	if in.pasting || in.ed.String() != "hello" {
		t.Fatalf("a paste end split across reads should end the paste: pasting=%v buf=%q", in.pasting, in.ed.String())
	}
	if res := in.handle([]byte("\r")); len(res.send) != 1 {
		t.Error("Enter should send after the paste ends")
	}
	out := chatFormat(chatMessage{Name: "#1 x\x1b]0;evil\x07", Text: "copy \x1b]52;c;ZXZpbA==\x07 this", At: time.Now()}, 80)
	if strings.Contains(out, "\x1b]") || strings.Contains(out, "\x07") {
		t.Errorf("control sequences from a message reached the terminal: %q", out)
	}
	t.Setenv("HOME", t.TempDir())
	_ = withChatLock(func(r *chatRoom) error {
		r.Members = []chatMember{{Key: "a", Name: "#10 claude", Engine: "claude"}}
		return nil
	})
	said := ""
	chatSubmit("@13 are you there?", func(s string) { said = s }, func() {})
	if len(readChat()) != 0 || !strings.Contains(said, "not sent") {
		t.Errorf("an @mention matching nobody should not be sent to everyone: msgs=%d said=%q", len(readChat()), said)
	}
}

// An engine is recognised by its program name, not by "claude" appearing somewhere in a path.
func TestEngineProgramIsMatchedByName(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--resume", "x"}, "claude"},
		{[]string{"/opt/homebrew/bin/codex"}, "codex"},
		{[]string{"node", "/usr/local/lib/node_modules/@openai/codex/bin/codex"}, "codex"},
		{[]string{"/opt/homebrew/bin/node", "/opt/homebrew/lib/node_modules/@openai/codex/bin/codex.js"}, "codex"},
		{[]string{"node", "/Users/me/.claude/plugins/mcp-server.cjs"}, ""},
		{[]string{"/private/tmp/claude-501/scratch/ptln", "loop", "approve"}, ""},
		{[]string{"/bin/zsh", "-c", "cd ~/.claude && ls"}, ""},
		{[]string{"/Users/me/dev/claude-tools/run"}, ""},
		{[]string{"claude-code-helper"}, ""},
		{nil, ""},
	} {
		if got := engineProgram(c.argv); got != c.want {
			t.Errorf("engineProgram(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}
