package main

import (
	"strings"
	"testing"
)

// The engine's answer to a revision turn IS an entire HTML document. Streamed unfiltered, it buries
// the handful of lines that tell a waiting human what is happening — and scrolls them away at the
// exact moment they are watching to see whether their mark was understood.
func TestReviewSinkHidesDocumentContent(t *testing.T) {
	var got []string
	sink := reviewSink(func(s string) { got = append(got, s) })

	for _, line := range []string{
		"reading the current markup",
		"```html",
		"<!doctype html><html><head><style>",
		"body{font:15px system-ui;margin:0}",
		"article{border:1px solid #ddd;}",
		"</style></head><body>",
		"<article><h2>Free</h2></article>",
		"```",
		"done",
	} {
		sink(line)
	}

	joined := strings.Join(got, "\n")
	for _, leaked := range []string{"doctype", "<article", "border:", "</style>"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("document content leaked into the activity stream (%q):\n%s", leaked, joined)
		}
	}
	if !strings.Contains(joined, "reading the current markup") {
		t.Errorf("progress before the document was dropped:\n%s", joined)
	}
	// Deliberate: document mode is sticky, so trailing prose is suppressed too. The document is the
	// last thing the model produces and IS the answer — a filter that unlatched on a closing fence
	// leaked the whole page whenever the opening fence did not arrive as its own line, which is what
	// a real turn actually does.
	if strings.Contains(joined, "done") {
		t.Errorf("document mode should stay latched to the end of the turn:\n%s", joined)
	}
	if strings.Count(joined, "writing the revised document") != 1 {
		t.Errorf("expected exactly one collapsed line for the document:\n%s", joined)
	}
}

// Models emit bare markup as often as fenced markup; the fence cannot be the only trigger.
func TestReviewSinkHidesUnfencedMarkup(t *testing.T) {
	var got []string
	sink := reviewSink(func(s string) { got = append(got, s) })
	sink("here is the revised page")
	sink("<!doctype html>")
	sink("<main class=\"tiers\">")
	sink("h2{margin:0}")

	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "doctype") || strings.Contains(joined, "<main") {
		t.Errorf("unfenced markup leaked:\n%s", joined)
	}
	if !strings.Contains(joined, "here is the revised page") {
		t.Errorf("prose before the document was dropped:\n%s", joined)
	}
}

func TestReviewSinkDropsBlankLines(t *testing.T) {
	n := 0
	sink := reviewSink(func(string) { n++ })
	sink("")
	sink("   ")
	if n != 0 {
		t.Errorf("blank lines should not reach the activity rail, got %d", n)
	}
}

// Watching a real turn taught this one: the opening ``` fence does not reliably arrive as its own
// line, so document mode cannot depend on seeing it. Once the document starts, everything after it
// is suppressed regardless of how it began.
func TestReviewSinkLatchesWithoutAnOpeningFence(t *testing.T) {
	var got []string
	sink := reviewSink(func(s string) { got = append(got, s) })
	sink("thinking")
	sink(`<!doctype html><html><head><style>`) // arrives with no fence line before it
	sink("body{font:15px system-ui;margin:0}") // ends in } — the case that leaked in the live run
	sink("main{display:grid}")
	sink("</style></head><body>")

	joined := strings.Join(got, "\n")
	for _, leaked := range []string{"doctype", "font:", "display:grid", "</style>"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("document content leaked (%q):\n%s", leaked, joined)
		}
	}
	if !strings.Contains(joined, "thinking") {
		t.Errorf("progress before the document was dropped:\n%s", joined)
	}
}

// Engines emit the same step marker many times in a row; repetition carries no information and
// pushes the informative lines out of view.
func TestReviewSinkCollapsesRepeats(t *testing.T) {
	var got []string
	sink := reviewSink(func(s string) { got = append(got, s) })
	for i := 0; i < 11; i++ {
		sink("· thinking_tokens")
	}
	sink("reading pricing.tsx")
	if len(got) != 2 {
		t.Errorf("expected the repeat collapsed to one line plus the new one, got %d: %v", len(got), got)
	}
}
