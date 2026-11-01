package engine

import (
	"strings"
	"testing"
)

func TestParseResultClaude(t *testing.T) {
	spec, _ := Lookup("claude")

	t.Run("full envelope", func(t *testing.T) {
		out := []byte(`
	{"result":"done: added the toggle","session_id":"sess-123","total_cost_usd":0.042,
	 "usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":40}}
`)
		res, err := spec.ParseResult(out)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Text != "done: added the toggle" || res.SessionID != "sess-123" {
			t.Errorf("Text/SessionID = %q/%q", res.Text, res.SessionID)
		}
		if got := res.Usage.Total(); got != 100 {
			t.Errorf("Usage.Total() = %d, want 100", got)
		}
		if res.CostUSD != 0.042 {
			t.Errorf("CostUSD = %v, want 0.042", res.CostUSD)
		}
	})

	t.Run("empty object is valid", func(t *testing.T) {
		// Matches the old parseWorkerOutput semantics: any well-formed JSON of the
		// envelope's shape parses (fields default), only malformed JSON errors.
		res, err := spec.ParseResult([]byte(`{}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Text != "" || res.SessionID != "" || res.Usage.Total() != 0 {
			t.Errorf("want zero Result, got %+v", res)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		for _, bad := range []string{"", "not json at all", `{"result": "truncat`, "error: claude crashed"} {
			if _, err := spec.ParseResult([]byte(bad)); err == nil {
				t.Errorf("ParseResult(%q): want error, got nil", bad)
			}
		}
	})
}

func TestParseResultPassthrough(t *testing.T) {
	raw := "plain text reply\nwith two lines\n"
	for _, name := range []string{"codex", "gemini", "antigravity"} {
		spec, _ := Lookup(name)
		res, err := spec.ParseResult([]byte(raw))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if res.Text != raw {
			t.Errorf("%s: Text = %q, want verbatim stdout %q", name, res.Text, raw)
		}
		if res.SessionID != "" || res.Usage.Total() != 0 || res.CostUSD != 0 {
			t.Errorf("%s: want no session/usage/cost, got %+v", name, res)
		}
		// Even JSON passes through verbatim for non-claude engines.
		res, _ = spec.ParseResult([]byte(`{"result":"x"}`))
		if !strings.Contains(res.Text, `"result"`) {
			t.Errorf("%s: JSON stdout must pass through verbatim, got %q", name, res.Text)
		}
	}
}
