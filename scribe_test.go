package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A minimal claude-shaped jsonl: user + assistant lines (+ a noise line the parser must skip).
const sampleClaudeJSONL = `{"type":"user","message":{"role":"user","content":"we will use Postgres for the store"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"got it, Postgres it is"}]}}
{"type":"summary","summary":"compacted"}
{"type":"user","message":{"role":"user","content":"the auth middleware double-runs — constraint"}}
`

func TestReadTranscriptSlice(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	turns, off, err := readTranscriptSlice(p, 0, claudeLineMsg)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 { // 2 user + 1 assistant; the summary line is skipped
		t.Fatalf("want 3 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("unexpected roles: %+v", turns)
	}
	if int(off) != len(sampleClaudeJSONL) {
		t.Fatalf("watermark should be at EOF (%d), got %d", len(sampleClaudeJSONL), off)
	}
	// Re-reading from the watermark yields nothing new (idempotent forward read).
	turns2, off2, _ := readTranscriptSlice(p, off, claudeLineMsg)
	if len(turns2) != 0 || off2 != off {
		t.Fatalf("re-read past watermark should be empty; got %d turns, off %d", len(turns2), off2)
	}
}

func TestReadTranscriptSliceIgnoresPartialTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	// A final line with no trailing newline = still being written; must be left for the next pass.
	body := `{"type":"user","message":{"role":"user","content":"complete line"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"half-written`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	turns, off, _ := readTranscriptSlice(p, 0, claudeLineMsg)
	if len(turns) != 1 {
		t.Fatalf("want only the 1 complete line, got %d", len(turns))
	}
	if int(off) >= len(body) {
		t.Fatalf("watermark must stop before the partial tail; off=%d len=%d", off, len(body))
	}
}

func TestReadTranscriptSliceBoundsReadWindow(t *testing.T) {
	// Shrink the window so a modest file overflows it; the pass must read only the tail and still
	// advance the watermark to EOF (skipped head turns are intentionally dropped, not re-read forever).
	orig := scribeMaxRead
	scribeMaxRead = 200
	defer func() { scribeMaxRead = orig }()

	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	var sb strings.Builder
	for i := 0; i < 40; i++ { // ~40 lines well over the 200-byte window
		sb.WriteString(`{"type":"user","message":{"role":"user","content":"padding line to overflow the read window"}}` + "\n")
	}
	sb.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"TAIL-marker"}]}}` + "\n")
	body := sb.String()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	turns, off, err := readTranscriptSlice(p, 0, claudeLineMsg)
	if err != nil {
		t.Fatal(err)
	}
	if int(off) != len(body) {
		t.Fatalf("watermark must reach EOF (%d) even when the head is skipped, got %d", len(body), off)
	}
	if len(turns) == 40+1 {
		t.Fatalf("expected the head to be skipped, but got every turn (%d)", len(turns))
	}
	last := turns[len(turns)-1]
	if !strings.Contains(last.Text, "TAIL-marker") {
		t.Fatalf("the recent tail must survive the window skip; last turn = %q", last.Text)
	}
}

func TestParseScribeFacts(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  int
	}{
		{"bare array", `[{"kind":"decision","body":"use Postgres","entities":["store"]}]`, 1},
		{"fenced", "sure:\n```json\n[{\"kind\":\"constraint\",\"body\":\"auth double-runs\"}]\n```\n", 1},
		{"prose around", `Here you go: [{"kind":"contract","body":"API returns 200"}] done`, 1},
		{"drops bad kind + empty body", `[{"kind":"chitchat","body":"hi"},{"kind":"decision","body":""},{"kind":"decision","body":"keep this"}]`, 1},
		{"empty array", `[]`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts, err := parseScribeFacts(c.reply)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(facts) != c.want {
				t.Fatalf("want %d facts, got %d: %+v", c.want, len(facts), facts)
			}
		})
	}
	if _, err := parseScribeFacts("not json at all"); err == nil {
		t.Fatal("want error on unparseable output")
	}
}

func TestRenderTranscriptCapsToTail(t *testing.T) {
	turns := []scribeTurn{
		{Role: "user", Text: "OLDEST-oldest"},
		{Role: "assistant", Text: "middle"},
		{Role: "user", Text: "NEWEST-newest"},
	}
	out := renderTranscript(turns, 40)
	if len(out) > 80 {
		t.Fatalf("expected a bounded render, got %d bytes", len(out))
	}
	if !strings.Contains(out, "NEWEST") {
		t.Fatalf("tail (newest) must survive truncation: %q", out)
	}
}

func TestWatermarkRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeErr := writeScribeState("sess-abc", scribeState{ThreadID: "t1", Offset: 42})
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	got := readScribeState("sess-abc")
	if got.ThreadID != "t1" || got.Offset != 42 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// sanitize keeps a path traversal from escaping the scribe dir.
	if sanitizeSessionID("../../etc/passwd") == "../../etc/passwd" {
		t.Fatal("sanitizeSessionID must strip path separators")
	}
}
