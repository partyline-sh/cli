package ptysess

import (
	"bytes"
	"testing"
)

func TestNormalizeCtrlBackslash(t *testing.T) {
	cases := map[string][]byte{
		"csiu":           []byte("\x1b[92;5u"),
		"modOther":       []byte("\x1b[27;5;92~"),
		"embedded":       append([]byte("ab\x1b[92;5u"), 'c'),
		"plain1c":        {0x1c},
		"kittySubParam":  []byte("\x1b[92;5:1u"),  // kitty event-type sub-param
		"ctrlShift":      []byte("\x1b[92;7u"),    // ctrl+shift held (mods-1 = 6, ctrl bit set)
		"ctrlAlt":        []byte("\x1b[92;13u"),   // ctrl+alt+shift, ctrl bit still set
		"modOtherCtrlSh": []byte("\x1b[27;7;92~"), // modifyOtherKeys ctrl+shift
		"twoInChunk":     []byte("\x1b[92;5ux\x1b[92;5u"),
	}
	for name, in := range cases {
		out := NormalizeCtrlBackslash(in)
		if !bytes.Contains(out, []byte{PrefixKey}) {
			t.Errorf("%s: expected 0x1c after normalize, got %v", name, out)
		}
	}
	// no false positives:
	noMatch := map[string][]byte{
		"upArrow":     []byte("\x1b[A"),
		"plainBacksl": []byte("\x1b[92u"),   // backslash with NO modifier — not the prefix
		"shiftOnly":   []byte("\x1b[92;2u"), // shift only (mods-1 = 1), no ctrl bit
		"otherKey":    []byte("\x1b[97;5u"), // ctrl-a, not ctrl-\
	}
	for name, in := range noMatch {
		if got := NormalizeCtrlBackslash(in); bytes.Contains(got, []byte{PrefixKey}) {
			t.Errorf("%s wrongly normalized to prefix: %v", name, got)
		}
	}

	// kitty "report event types": ctrl-\ release/repeat must be DROPPED entirely
	// (not turned into a second 0x1c, which would swallow the prefix). This was the
	// bug — the release `\x1b[92;5:3u` was normalizing to 0x1c.
	drop := map[string][]byte{
		"release": []byte("\x1b[92;5:3u"),
		"repeat":  []byte("\x1b[92;5:2u"),
	}
	for name, in := range drop {
		if got := NormalizeCtrlBackslash(in); len(got) != 0 {
			t.Errorf("%s: expected ctrl-\\ %s event dropped (empty), got %v", name, name, got)
		}
	}

	// A real press+release pair (as the terminal sends them) yields exactly ONE
	// prefix byte, not two.
	pair := NormalizeCtrlBackslash([]byte("\x1b[92;5u\x1b[92;5:3u"))
	if len(pair) != 1 || pair[0] != PrefixKey {
		t.Errorf("press+release should yield a single 0x1c, got %v", pair)
	}
}
