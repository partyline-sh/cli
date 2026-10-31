package main

import (
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// The metric itself is tested in internal/brand. What matters here is that the cg palette's
// escapes (cgKey/cgDim/cgOff) are invisible to it, so a boxed menu's border lands on the widest
// LABEL, not the widest byte count — the box used to fray whenever a row gained a colour.
func TestCgRowMeasuresLabelsNotEscapes(t *testing.T) {
	plain := "    r  record a fact"
	if got, want := brand.VisWidth(cgRow("r", "record a fact", "")), brand.VisWidth(plain); got != want {
		t.Errorf("cgRow width = %d, want %d (escapes must not count)", got, want)
	}
	noted := cgRow("r", "record a fact", "writes as you")
	if brand.VisWidth(noted) <= brand.VisWidth(cgRow("r", "record a fact", "")) {
		t.Errorf("a noted row should be wider: %q", noted)
	}
	// A row clipped for a narrow box never exceeds the box interior.
	for w := 4; w <= 40; w++ {
		if got := brand.VisWidth(brand.ClipEllipsis(noted, w)); got > w {
			t.Errorf("clipped row at %d has width %d", w, got)
		}
	}
}
