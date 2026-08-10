package ptymux

import "testing"

// The bar row survives only if nothing hands it back to the scrolling area. These are the shapes
// that would.
func TestClampScrollRegion(t *testing.T) {
	const body = 23 // a 24-row terminal, bottom row reserved
	cases := []struct {
		name, in, want string
	}{
		{"full reset would un-pin the row", "\x1b[r", "\x1b[1;23r"},
		{"region reaching the reserved row is clamped", "\x1b[1;24r", "\x1b[1;23r"},
		{"a stale post-resize region is clamped", "\x1b[1;40r", "\x1b[1;23r"},
		{"a region inside the body is left alone", "\x1b[1;23r", "\x1b[1;23r"},
		{"a child's pinned input box is left alone", "\x1b[3;20r", "\x1b[3;20r"},
		{"clamping preserves the top margin", "\x1b[5;30r", "\x1b[5;23r"},
		{"surrounding output is untouched", "hi\x1b[rthere", "hi\x1b[1;23rthere"},
		{"multiple regions in one write", "\x1b[r-\x1b[1;99r", "\x1b[1;23r-\x1b[1;23r"},
		{"no escape at all", "just text\n", "just text\n"},
		{"a private mode is not DECSTBM", "\x1b[?25l", "\x1b[?25l"},
		{"an unrelated CSI is untouched", "\x1b[2J\x1b[H", "\x1b[2J\x1b[H"},
		{"a malformed run passes through rather than being guessed at", "\x1b[1;2;3r", "\x1b[1;2;3r"},
		{"a truncated escape at the end is left alone", "text\x1b[", "text\x1b["},
	}
	for _, c := range cases {
		if got := string(clampScrollRegion([]byte(c.in), body)); got != c.want {
			t.Errorf("%s:\n  in   %q\n  got  %q\n  want %q", c.name, c.in, got, c.want)
		}
	}
}

// A write with nothing to rewrite must return the ORIGINAL slice — this runs on every byte the
// child emits, so allocating per write would be a real cost on a chatty agent.
func TestClampAvoidsAllocatingOnTheCommonPath(t *testing.T) {
	in := []byte("ordinary agent output with no region change\n")
	out := clampScrollRegion(in, 23)
	if &out[0] != &in[0] {
		t.Error("clampScrollRegion copied a slice it did not need to change")
	}
}

// A nonsense body height must not produce a region that hides the screen.
func TestClampIsInertWithoutABody(t *testing.T) {
	in := "\x1b[r"
	for _, body := range []int{0, -1} {
		if got := string(clampScrollRegion([]byte(in), body)); got != in {
			t.Errorf("body %d: got %q, want the input untouched", body, got)
		}
	}
	if got := string(scrollRegionFor(0)); got != "\x1b[1;1r" {
		t.Errorf("scrollRegionFor(0) = %q, want a 1-row floor", got)
	}
}
