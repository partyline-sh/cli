package brand

import (
	"strings"
	"testing"
)

// These three functions lay out every frame in the app. A one-column error here does not fail
// loudly — it frays a box border or shifts a row on some surface nobody thought to check — so
// the cases below pin the awkward inputs: wide runes, zero-width combining marks, embedded SGR,
// and non-SGR CSI sequences (\x1b[K) that the old per-package metrics mis-scanned.
func TestVisWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ascii", "claude", 6},
		{"empty", "", 0},
		{"wide emoji", "⏳", 2},
		{"ambiguous shapes", "●·▸", 3},
		{"marker parity a", "▸ ", 2},
		{"marker parity b", "  ", 2}, // MUST equal "▸ " or the picker box resizes as you move
		{"cjk", "日本語", 6},
		{"cjk mixed", "a日b", 4},
		{"combining acute", "é", 1}, // e + COMBINING ACUTE = one column
		{"combining many", "á̂̃", 1},
		{"sgr wrapped", "\x1b[38;5;245mwaiting\x1b[0m", 7},
		{"sgr bold marker", "\x1b[1m▸ \x1b[0m", 2},
		{"sgr truecolor", "\x1b[38;2;255;94;138mhi\x1b[0m", 2},
		{"csi erase line", "ab\x1b[Kcd", 4}, // \x1b[K terminates at K, not at a later 'm'
		{"csi cup then sgr", "\x1b[3;1Hx\x1b[1mm\x1b[0m", 2},
		{"decsc two byte", "\x1b7ab\x1b8", 2},
		{"osc title bel", "\x1b]0;title\x07ab", 2},
		{"mixed row", "1 ⏳ claude", 1 + 1 + 2 + 1 + 6},
	}
	for _, c := range cases {
		if got := VisWidth(c.in); got != c.want {
			t.Errorf("%s: VisWidth(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestClip(t *testing.T) {
	// Fits: returned untouched, no reset bolted on.
	if got := Clip("abc", 10); got != "abc" {
		t.Errorf("Clip fitting = %q, want %q", got, "abc")
	}
	if got := Clip("abc", 0); got != "" {
		t.Errorf("Clip to 0 = %q, want empty", got)
	}
	if got := Clip("abc", -3); got != "" {
		t.Errorf("Clip to negative = %q, want empty", got)
	}
	// Never wider than asked, on every awkward input.
	for _, in := range []string{
		"abcdefgh", "日本語です", "a日b日c", "ééé",
		"\x1b[31mabcdef\x1b[0m", "\x1b[38;5;245m日本語\x1b[0m", "a\x1b[Kbcdefg",
	} {
		for w := 1; w <= 8; w++ {
			got := Clip(in, w)
			if v := VisWidth(got); v > w {
				t.Errorf("Clip(%q, %d) = %q has width %d > %d", in, w, got, v, w)
			}
		}
	}
	// A wide rune straddling the edge is dropped whole — never half-rendered.
	if got := Clip("a日b", 2); VisWidth(got) != 1 {
		t.Errorf("Clip(\"a日b\", 2) = %q width %d, want 1 (the 日 must be dropped, not split)", got, VisWidth(got))
	}
	// Truncation always leaves the terminal with no colour bleeding into the next cell.
	if got := Clip("\x1b[31mabcdef", 3); !strings.HasSuffix(got, reset) {
		t.Errorf("Clip must reset when it cuts, got %q", got)
	}
	// Escapes are copied verbatim and never counted or severed.
	if got := Clip("\x1b[38;5;245mabcdef\x1b[0m", 3); !strings.Contains(got, "\x1b[38;5;245m") {
		t.Errorf("Clip dropped the leading SGR: %q", got)
	}
}

func TestClipEllipsis(t *testing.T) {
	if got := ClipEllipsis("abc", 10); got != "abc" {
		t.Errorf("ClipEllipsis fitting = %q, want untouched", got)
	}
	for _, in := range []string{"abcdefgh", "日本語です", "\x1b[31mabcdefgh\x1b[0m"} {
		for w := 2; w <= 7; w++ { // 8 would fit "abcdefgh" exactly — nothing to elide
			got := ClipEllipsis(in, w)
			if v := VisWidth(got); v > w {
				t.Errorf("ClipEllipsis(%q, %d) = %q has width %d > %d", in, w, got, v, w)
			}
			if !strings.Contains(got, "…") {
				t.Errorf("ClipEllipsis(%q, %d) = %q lost its ellipsis", in, w, got)
			}
		}
	}
}

func TestPadTo(t *testing.T) {
	for _, in := range []string{
		"", "ab", "abcdefgh", "日本語", "a日b", "éx",
		"\x1b[31mab\x1b[0m", "\x1b[38;5;245m日本語\x1b[0m",
	} {
		for w := 0; w <= 10; w++ {
			got := PadTo(in, w)
			if v := VisWidth(got); v != w {
				t.Errorf("PadTo(%q, %d) = %q has width %d, want exactly %d", in, w, got, v, w)
			}
		}
	}
	// Clipping can land a column short (dropped wide rune) — PadTo must still hit w exactly.
	if v := VisWidth(PadTo("日日日", 3)); v != 3 {
		t.Errorf("PadTo over a straddling wide rune = width %d, want 3", v)
	}
}
