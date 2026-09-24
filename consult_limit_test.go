package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// THE TWO COPIES OF THE QUESTION BOUND CANNOT DRIFT. The server refuses an over-long question in ONE
// gate (web/src/lib/api/consult-open.ts, MAX_QUESTION); the CLI carries the same number so it can
// refuse locally and say what to cut. Two numbers in two languages is exactly the pair that silently
// diverges, so this test reads the TypeScript and fails if they disagree — the comment in each file
// points at the other, and this is what makes the pointer enforceable.
func TestQuestionBoundMatchesTheServerGate(t *testing.T) {
	b, err := os.ReadFile("web/src/lib/api/consult-open.ts")
	if err != nil {
		t.Fatalf("the server gate must be readable from here — if it moved, fix this test and the comments in both files: %v", err)
	}
	m := regexp.MustCompile(`(?m)^export const MAX_QUESTION = (\d+);`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("couldn't find `export const MAX_QUESTION = <n>;` in consult-open.ts")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if n != maxQuestionChars {
		t.Fatalf("MAX_QUESTION is %d server-side but maxQuestionChars is %d — the CLI would either refuse "+
			"questions the server accepts, or send ones it 400s", n, maxQuestionChars)
	}
	// The gate must still be the only place that enforces it: openConsult, not the route.
	if !strings.Contains(string(b), "question.length > MAX_QUESTION") {
		t.Error("consult-open.ts no longer enforces MAX_QUESTION — the shared gate is the only place it may live")
	}
}

// questionLen counts what the SERVER counts: UTF-16 code units, because the gate is JavaScript's
// String.length. Bytes and code points both disagree with it, and a wrong count makes "trim 12" a lie.
func TestQuestionLenCountsTheWayJavaScriptDoes(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"abc", 3},
		{"héllo", 5},        // 6 bytes, 5 units
		{"日本語", 3},          // 9 bytes, 3 units
		{"a\U0001F600b", 4}, // an emoji is a SURROGATE PAIR: 2 units, not 1 rune
	}
	for _, c := range cases {
		if got := questionLen(c.s); got != c.want {
			t.Errorf("questionLen(%q) = %d, want %d", c.s, got, c.want)
		}
	}
	if questionOverBy(strings.Repeat("x", maxQuestionChars)) != 0 {
		t.Error("exactly the limit is not over it")
	}
	if got := questionOverBy(strings.Repeat("x", maxQuestionChars+9)); got != 9 {
		t.Errorf("questionOverBy = %d, want 9", got)
	}
}

func TestCommaNumGroupsThousands(t *testing.T) {
	for _, c := range [][2]string{{"0", "0"}, {"999", "999"}, {"1000", "1,000"}, {"32000", "32,000"}, {"1234567", "1,234,567"}} {
		n, _ := strconv.Atoi(c[0])
		if got := commaNum(n); got != c[1] {
			t.Errorf("commaNum(%s) = %s, want %s", c[0], got, c[1])
		}
	}
}
