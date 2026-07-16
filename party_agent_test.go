package main

import "testing"

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
