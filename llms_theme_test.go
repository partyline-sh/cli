package main

import "testing"

func TestThemedRemap(t *testing.T) {
	orig := theme
	defer func() { theme = orig }()

	// Midnight = identity: a frame must pass through byte-for-byte.
	theme = themes[0]
	in := "\x1b[38;5;250mproj\x1b[0m \x1b[48;5;236mrow\x1b[0m"
	if got := themed(in); got != in {
		t.Errorf("Midnight not identity:\n in=%q\ngot=%q", in, got)
	}

	// Daylight: the unreadable light-grey label (250) must become a dark index, and the
	// selection bg (236) a light tint — proving fg AND bg indexes are remapped.
	for i := range themes {
		if themes[i].name == "Daylight" {
			theme = themes[i]
		}
	}
	got := themed("\x1b[38;5;250mlabel\x1b[0m")
	if got != "\x1b[38;5;238mlabel\x1b[0m" {
		t.Errorf("Daylight fg 250→238 failed: got %q", got)
	}
	if got := themed("\x1b[48;5;236m"); got != "\x1b[48;5;153m" {
		t.Errorf("Daylight bg 236→153 failed: got %q", got)
	}
	// Bold + fg combined sequence: bold must survive, index remapped.
	if got := themed("\x1b[1;38;5;231m"); got != "\x1b[1;38;5;16m" {
		t.Errorf("combined SGR remap failed: got %q", got)
	}
	// An unmapped index passes through.
	if got := themed("\x1b[38;5;99m"); got != "\x1b[38;5;99m" {
		t.Errorf("unmapped index changed: got %q", got)
	}
}
