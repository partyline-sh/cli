package main

import (
	"fmt"
	"unicode/utf16"
)

// THE QUESTION BOUND, ONCE.
//
// The server gate is web/src/lib/api/consult-open.ts (MAX_QUESTION) — the ONE place a consult is
// opened, shared by the REST route and the A2A gateway, so there is exactly one place that can refuse
// an over-long question. This file is the CLI's copy of that number, and it exists because finding out
// from a 400 AFTER composing several thousand words is a terrible way to learn you were over: the
// compose field shows the count live, and every ask checks locally before it sends.
//
// TWO COPIES OF A NUMBER DRIFT. So they are pinned together by a test rather than by a comment:
// TestQuestionBoundMatchesTheServerGate (consult_limit_test.go) parses MAX_QUESTION out of
// consult-open.ts and fails if it isn't this value. Change one and the build tells you about the other.
//
// WHY 32000. An LLM discussion excerpt is the input this field was built for, and 8000 chars (~2k
// tokens) is roughly one long answer — the old bound rejected the ordinary case. 32000 chars is ~8k
// tokens: comfortably a whole design discussion, still a small fraction of any answering engine's
// context window, and still bounded (the row is `text`, but the read-only answer turn has to fit the
// question in a prompt, and the daily auto-answer caps are per-question, not per-token).
const maxQuestionChars = 32000

// questionLen counts a question the way the SERVER counts it. The gate is JavaScript `String.length`
// — UTF-16 code units — so len() (bytes) and len([]rune) (code points) would both disagree with it on
// any non-ASCII text, and the CLI's "you're 12 over" would be a different 12 from the server's. Emoji
// and CJK in a pasted excerpt are not hypothetical.
func questionLen(q string) int { return len(utf16.Encode([]rune(q))) }

// questionOverBy reports how many units a question is over the bound, 0 when it fits.
func questionOverBy(q string) int {
	if n := questionLen(q); n > maxQuestionChars {
		return n - maxQuestionChars
	}
	return 0
}

// questionTooLongNote is the ONE sentence every CLI surface uses when a question is over. It names the
// three numbers that matter — what you have, what is allowed, and what to cut — because "too long" on
// its own leaves you editing blind. Empty string means it fits.
func questionTooLongNote(q string) string {
	over := questionOverBy(q)
	if over == 0 {
		return ""
	}
	return fmt.Sprintf("that question is %s characters; the limit is %s — trim %s and try again",
		commaNum(questionLen(q)), commaNum(maxQuestionChars), commaNum(over))
}

// commaNum groups an integer with thousands separators. Five digits of character count are unreadable
// without them, and this number's whole job is to be read at a glance.
func commaNum(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
