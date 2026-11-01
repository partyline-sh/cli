package theme

import (
	"github.com/mattn/go-runewidth"
	"testing"
)

func TestSignalWidthAndStaticFrames(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		for _, state := range []string{"working", "waiting", "done", "failed", "idle"} {
			for frame := 0; frame < 16; frame++ {
				got := Signal(state, frame, ascii, true)
				if runewidth.StringWidth(got) != 7 {
					t.Fatalf("signal width %q", got)
				}
				if ascii {
					for _, r := range got {
						if r > 127 {
							t.Fatalf("non-ASCII: %q", got)
						}
					}
				}
				if state != "working" && got != Signal(state, 0, ascii, true) {
					t.Fatal("static state changes between frames")
				}
				if Signal(state, frame, ascii, false) != Signal(state, 0, ascii, false) {
					t.Fatal("reduced motion changes frame")
				}
			}
		}
	}
	if Signal("working", 0, false, true) == Signal("working", 1, false, true) {
		t.Fatal("working meter does not move")
	}
}
