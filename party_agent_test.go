package main

import "testing"

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
