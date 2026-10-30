package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The screen states, against lines captured from Claude Code 2.1.310 and codex 0.163.0.
func TestChatScreenActivity(t *testing.T) {
	claudeWorking := "⏺ Bash(go test ./...)\n  ⎿  Running…\n✽ Ebbing… (39s · ↓ 2.3k tokens)\n──────\n❯ \n──────\n  assetmgmt (main) Opus 5.5"
	claudeLong := "✻ Brewing… (2m 3s · ↑ 12k tokens · esc to interrupt)\n❯ "
	codexWorking := "• Working (1m 12s • esc to interrupt)\n› Ask Codex to do anything"
	claudeApproval := "Bash command\n  rm -rf build\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again for rm commands\n  3. No, and tell Claude what to do differently (esc)"
	codexApproval := "Would you like to run the following command?\n  $ go test ./...\n› 1. Yes, proceed (y)\n  2. Yes, and don't ask again (a)\n  3. No, and tell Codex what to do differently (esc)"
	idle := "※ recap: thumbnails are live\n──────\n❯ \n──────\n  xero-receipts (main) Opus 5.5"
	cases := []struct {
		name, screen, state, verb, elapsed string
	}{
		{"claude working", claudeWorking, "working", "Ebbing", "39s"},
		{"claude working, minutes", claudeLong, "working", "Brewing", "2m 3s"},
		{"codex working", codexWorking, "working", "Working", "1m 12s"},
		{"claude approval", claudeApproval, "approval", "", ""},
		{"codex approval", codexApproval, "approval", "", ""},
		{"idle", idle, "idle", "", ""},
	}
	for _, c := range cases {
		a := screenActivity(c.screen)
		if a.State != c.state || a.Verb != c.verb || a.Elapsed != c.elapsed {
			t.Errorf("%s: got %+v, want state=%s verb=%s elapsed=%s", c.name, a, c.state, c.verb, c.elapsed)
		}
	}
	// The wake rule uses the same detection: a working Claude session is never typed into.
	if ok, why := screenIdleEmpty(claudeWorking); ok || why != "it is working" {
		t.Errorf("Claude's current working line must count as busy for the wake: %v %q", ok, why)
	}
	if ok, why := screenIdleEmpty(claudeApproval); ok || why != "it is waiting for your approval" {
		t.Errorf("a permission prompt must not be typed into: %v %q", ok, why)
	}
}

func TestChatToolSummaries(t *testing.T) {
	in := func(s string) json.RawMessage { return json.RawMessage(s) }
	cases := map[string]string{
		summariseTool("Bash", in(`{"command":"go test ./...","description":"Run the tests"}`)): "Run the tests",
		summariseTool("Bash", in(`{"command":"go vet ./..."}`)):                                "running go vet ./...",
		summariseTool("Edit", in(`{"file_path":"/repo/internal/web/render.go"}`)):              "editing render.go",
		summariseTool("Grep", in(`{"pattern":"chatPane"}`)):                                    "searching for chatPane",
		summariseTool("TodoWrite", in(`{}`)):                                                   "using TodoWrite",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	m := codexCmd.FindStringSubmatch(`text(await tools.exec_command({cmd:"go test -race ./... && echo \"ok\"","max_output_tokens":5000}));`)
	if m == nil || m[1] != `go test -race ./... && echo \"ok\"` {
		t.Errorf("codex exec_command not parsed: %v", m)
	}
}

// Priority and wording of the per-member line, including during a pause.
func TestChatStatusShowsActivity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	r := &chatRoom{AgentRun: chatAgentRunCap, Members: []chatMember{
		{Key: "a", Name: "#10 claude", Engine: "claude", Read: 2},
		{Key: "b", Name: "#11 codex", Engine: "codex", Read: 1},
		{Key: "c", Name: "#12 claude", Engine: "claude", Read: 1},
	}}
	msgs := []chatMessage{{Seq: 1}, {Seq: 2, From: "you", Text: "status?"}}
	act := map[string]memberActivity{
		"a": {State: "working", Verb: "Ebbing", Elapsed: "2m 3s", Doing: "running go test ./..."},
		"b": {State: "approval", Doing: "running rm -rf build"},
	}
	lines, spinning := chatStatusLines(r, msgs, nil, act, now)
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		"#10 claude · working in its tab · Ebbing 2m 3s · running go test ./...",
		"‼ #11 codex · waiting for your approval in tab 11 · running rm -rf build",
		"#12 claude · idle, 1 message waiting (delivery paused)",
		"they wait for you to say something",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if !spinning {
		t.Error("a working member should animate")
	}
	if got := stripColours(chatFreeNote("#10 claude", 6*time.Minute+12*time.Second)); got != "#10 claude is free (worked 6m12s)" {
		t.Errorf("free note: %q", got)
	}
}

// Review fixes: a menu with the cursor on another option is still an approval prompt; a Markdown
// quote is not; and the wake check sees a working line pushed up by a long todo list.
func TestChatActivityReviewFixes(t *testing.T) {
	moved := "Do you want to proceed?\n  1. Yes\n❯ 2. Yes, and don't ask again\n  3. No"
	if a := screenActivity(moved); a.State != "approval" {
		t.Errorf("a menu with the cursor on option 2 is still a prompt: %+v", a)
	}
	quote := "⏺ Here is the plan\n> 1. Yes, proceed with the migration\n  2. Then deploy\n❯ "
	if a := screenActivity(quote); a.State == "approval" {
		t.Error("a Markdown quote must not read as a permission menu")
	}
	var todo strings.Builder
	todo.WriteString("✻ Brewing… (40s · esc to interrupt)\n")
	for i := 0; i < 14; i++ {
		todo.WriteString("  ☐ step " + string(rune('a'+i)) + "\n")
	}
	todo.WriteString("──────\n❯ \n──────\n  repo (main) Opus\n  ⏵⏵ bypass permissions on")
	screen := todo.String()
	if a := screenActivity(screenBottom(screen, 15)); a.State == "working" {
		t.Log("(the bottom 15 lines alone miss it, which is why the wake check reads the whole screen)")
	}
	if a := screenActivity(screen); a.State != "working" {
		t.Errorf("the whole visible screen should show the session working: %+v", a)
	}
}

// A member that read a message and chose not to reply, in a session with no chat Stop hook, kept
// its "working on your message" line and a timer that ran all night. After the grace period the
// line follows the screen.
func TestAnsweringLineFollowsTheScreenAfterGrace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	r := &chatRoom{Members: []chatMember{
		{Key: "a", Name: "#10 claude", Read: 2, Answering: true, Delivered: now.Add(-763 * time.Minute)},
		{Key: "b", Name: "#11 codex", Read: 2, Answering: true, Delivered: now.Add(-2 * time.Minute)},
	}}
	msgs := []chatMessage{{Seq: 1}, {Seq: 2, From: "you", Text: "what next?"}}
	act := map[string]memberActivity{"a": {State: "idle"}, "b": {State: "working", Verb: "Working", Elapsed: "2m 0s"}}
	lines, _ := chatStatusLines(r, msgs, nil, act, now)
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "#10 claude") {
		t.Errorf("idle member still shown as answering:\n%s", all)
	}
	if !strings.Contains(all, "#11 codex · "+chatVerb("b", 2)+"… 2m00s") {
		t.Errorf("working member lost its answering line:\n%s", all)
	}
}
