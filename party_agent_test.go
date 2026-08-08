package main

import (
	"partyline.sh/partyline/internal/api"

	"testing"
	"time"
)

// TestResolvePartyModel pins that a server-sent party model (settings.model) survives for
// EVERY engine (the old code wiped it for non-claude engines), that an explicit --model still
// wins, that claude alone falls back to the "haiku" alias, and that a non-token server value
// (remote input headed for an exec argv) is rejected by the modelRe shape gate.
func TestResolvePartyModel(t *testing.T) {
	cases := []struct {
		name, engine, cli string
		set               bool
		server, want      string
	}{
		{"server model survives for codex", "codex", "", false, "gpt-5.3-codex", "gpt-5.3-codex"},
		{"server model survives for gemini", "gemini", "", false, "gemini-3-pro", "gemini-3-pro"},
		{"server model survives for antigravity", "antigravity", "", false, "gemini-3-flash", "gemini-3-flash"},
		{"server model applies to claude", "claude", "", false, "opus", "opus"},
		{"cli --model beats the server model", "codex", "o4-mini", true, "gpt-5.3-codex", "o4-mini"},
		{"claude defaults to haiku", "claude", "", false, "", "haiku"},
		{"non-claude default stays empty", "codex", "", false, "", ""},
		{"flag-shaped server model ignored (claude keeps default)", "claude", "", false, "--dangerously-skip-permissions", "haiku"},
		{"injection-shaped server model ignored (codex)", "codex", "", false, "a b; rm -rf /", ""},
	}
	for _, tc := range cases {
		if got := resolvePartyModel(tc.engine, tc.cli, tc.set, tc.server); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestGrounded pins the grounding decision: server-authoritative (an `override` from the control
// plane wins), with a mode-based fallback for an older backend — but describe NEVER grounds, whichever
// way the flag lands (grounding overrides its propose_edit/question loop → the empty-doc "agent died"
// bug). It's a party-MODE property, not a launch-preset one.
func TestGrounded(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name         string
		override     *bool
		evidenceFlag bool
		mode         string
		want         bool
	}{
		// server override wins for non-describe modes
		{"server grounds chat", &yes, false, "chat", true},
		{"server ungrounds approach", &no, false, "approach", false},
		// describe is never grounded, whatever the server says or the flag is
		{"describe never grounds — server true", &yes, false, "describe", false},
		{"describe never grounds — --evidence", nil, true, "describe", false},
		{"describe never grounds — default", nil, false, "describe", false},
		// fallback (no server value): approach + explicit --evidence
		{"fallback: approach grounds", nil, false, "approach", true},
		{"fallback: chat does not ground", nil, false, "chat", false},
		{"fallback: --evidence grounds a non-describe mode", nil, true, "chat", true},
		{"fallback: project_setup does not ground", nil, false, "project_setup", false},
	}
	for _, tc := range cases {
		if got := grounded(tc.override, tc.evidenceFlag, tc.mode); got != tc.want {
			t.Errorf("%s: grounded(%v, %v, %q) = %v, want %v", tc.name, tc.override, tc.evidenceFlag, tc.mode, got, tc.want)
		}
	}
}

// TestClampTimeout pins that server-sent turn timeouts are bounded — a nil/zero/hostile value can
// never disable the turn guard (it snaps to the default or the [lo,hi] bounds).
func TestClampTimeout(t *testing.T) {
	def, lo, hi := 5*time.Minute, 30*time.Second, 15*time.Minute
	p := func(n int) *int { return &n }
	cases := []struct {
		name string
		sec  *int
		want time.Duration
	}{
		{"nil → default", nil, def},
		{"in range", p(120), 2 * time.Minute},
		{"zero clamps up to lo", p(0), lo},
		{"below lo clamps up", p(5), lo},
		{"above hi clamps down", p(99999), hi},
		{"negative clamps up to lo", p(-10), lo},
	}
	for _, tc := range cases {
		if got := clampTimeout(tc.sec, def, lo, hi); got != tc.want {
			t.Errorf("%s: clampTimeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExtractProposeEdits(t *testing.T) {
	t.Run("prose plus one edit", func(t *testing.T) {
		in := "Sounds good, updating the goals.\n\n```propose-edit section=Goals\nship A1\nship A2\n```\nLet me know."
		clean, edits := extractProposeEdits(in)
		if clean != "Sounds good, updating the goals.\n\nLet me know." {
			t.Errorf("clean = %q", clean)
		}
		if len(edits) != 1 || edits[0].section != "Goals" || edits[0].body != "ship A1\nship A2" {
			t.Errorf("edits = %#v", edits)
		}
	})

	t.Run("quoted multi-word section", func(t *testing.T) {
		in := "```propose-edit section=\"Open Questions\"\nq1\n```"
		clean, edits := extractProposeEdits(in)
		if clean != "" {
			t.Errorf("clean = %q, want empty", clean)
		}
		if len(edits) != 1 || edits[0].section != "Open Questions" || edits[0].body != "q1" {
			t.Errorf("edits = %#v", edits)
		}
	})

	t.Run("block without a section is dropped, not posted raw", func(t *testing.T) {
		in := "before\n```propose-edit\nno section here\n```\nafter"
		clean, edits := extractProposeEdits(in)
		if len(edits) != 0 {
			t.Errorf("edits = %#v, want none", edits)
		}
		if clean != "before\nafter" {
			t.Errorf("clean = %q", clean)
		}
	})

	t.Run("multiple blocks", func(t *testing.T) {
		in := "```propose-edit section=A\naaa\n```\n```propose-edit section=B\nbbb\n```"
		clean, edits := extractProposeEdits(in)
		if clean != "" || len(edits) != 2 || edits[0].section != "A" || edits[1].section != "B" {
			t.Errorf("clean=%q edits=%#v", clean, edits)
		}
	})

	t.Run("plain code fences are left alone", func(t *testing.T) {
		in := "here:\n```go\nfmt.Println(1)\n```\ndone"
		clean, edits := extractProposeEdits(in)
		if len(edits) != 0 || clean != in {
			t.Errorf("clean=%q edits=%#v", clean, edits)
		}
	})
}

// The join race. A message posted in the moments AFTER an agent announces itself but BEFORE its
// stream finishes connecting arrives in the initial replay, ahead of `: ready`. Gating purely on
// `live` filed it as history and the agent waited forever for a prompt it had already been handed —
// which is precisely what the describe view triggers, since it auto-sends its opening message the
// instant the agent appears.
//
// The gate is now "live OR newer than my own online post". This test drives the same predicate the
// event loop uses, so it fails if that expression is ever simplified back.
func TestWakesOnMessagesNewerThanJoinEvenBeforeReady(t *testing.T) {
	eligible := func(r *partyRunner, m api.PartyMsg) bool {
		return (r.live || (r.joinID > 0 && m.ID > r.joinID)) && r.shouldWake(m)
	}
	r := &partyRunner{name: "partyline", joinID: 1189, peers: map[string]bool{}}

	// Replay, still pre-`ready`.
	to := map[string]any{"to": []any{"partyline"}}
	history := api.PartyMsg{ID: 1100, Sender: "user:Darcy", Kind: "msg", Body: "@partyline old question", Meta: to}
	raced := api.PartyMsg{ID: 1190, Sender: "user:Darcy", Kind: "msg", Body: "@partyline just running a test", Meta: to}

	if eligible(r, history) {
		t.Error("replayed HISTORY must not wake the agent — that re-answers the whole transcript on restart")
	}
	if !eligible(r, raced) {
		t.Error("a message posted after our join must wake us even before `: ready` — this is the lost-message bug")
	}

	// Once live, the id no longer matters.
	r.live = true
	if !eligible(r, api.PartyMsg{ID: 1191, Sender: "user:Darcy", Kind: "msg", Body: "@partyline follow up", Meta: to}) {
		t.Error("a live message must still wake the agent")
	}

	// Our own posts never wake us, whatever their id — an agent replying to itself is a loop.
	if eligible(r, api.PartyMsg{ID: 1300, Sender: "agent:partyline", Kind: "msg", Body: "@partyline hello", Meta: to}) {
		t.Error("the agent must never wake on its own message")
	}

	// A runner with no watermark (post failed / older path) falls back to the old behaviour rather
	// than treating every replayed message as new.
	r2 := &partyRunner{name: "partyline", joinID: 0, peers: map[string]bool{}}
	if eligible(r2, raced) {
		t.Error("without a join watermark, pre-ready messages must stay backlog")
	}
}
