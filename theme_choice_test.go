package main

import (
	"os"
	"strings"
	"testing"

	uitheme "partyline.sh/partyline/internal/theme"
)

func TestParseOSC11(t *testing.T) {
	for reply, want := range map[string]struct{ dark, ok bool }{
		"\x1b]11;rgb:1b1b/1a1a/1919\x07":       {true, true},  // Partyline dark ground
		"\x1b]11;rgb:ffff/ffff/ffff\x1b\\":     {false, true}, // white, ST-terminated
		"\x1b]11;rgb:f/f/f\x07":                {false, true}, // one digit per channel
		"\x1b]11;rgb:13/17/1e\x07":             {true, true},  // two digits
		"\x1b]11;rgba:eeee/eeee/eeee/ffff\x07": {false, true},
		"\x1b]11;rgb:zz/zz/zz\x07":             {false, false},
		"garbage":                              {false, false},
		"":                                     {false, false},
	} {
		dark, ok := parseOSC11(reply)
		if dark != want.dark || ok != want.ok {
			t.Errorf("%q: dark=%v ok=%v, want %v %v", reply, dark, ok, want.dark, want.ok)
		}
	}
}

// Legacy names pin a palette and variant whatever the background; palette names follow it.
func TestThemeForNamesAndBackground(t *testing.T) {
	dark, light := themeBackground{Choice: "dark"}, themeBackground{Choice: "light"}
	for _, c := range []struct {
		name    string
		bg      themeBackground
		palette string
		dark    bool
	}{
		{"Midnight", light, "Partyline", true},
		{"Daylight", dark, "Partyline", false},
		{"Raging Unicorn 🦄", dark, "Partyline", false},
		{"Cotton Sky", dark, "Blue", false},
		{"Paperwhite", dark, "Paperwhite", false},
		{"Blue", dark, "Blue", true},
		{"Mono", light, "Mono", false},
		{"", themeBackground{Choice: "auto", Detected: "light"}, "Partyline", false},
		{"", themeBackground{Choice: "auto"}, "Partyline", true}, // never answered: dark
		{"nonsense", dark, "Partyline", true},
	} {
		got := themeFor(c.name, c.bg)
		if got.Palette != c.palette || got.Dark != c.dark {
			t.Errorf("%q with %+v: %s dark=%v, want %s dark=%v", c.name, c.bg, got.Palette, got.Dark, c.palette, c.dark)
		}
	}
}

// t cycles the four palettes; a legacy name continues from the palette it maps to.
func TestNextPalette(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for cur, next := range map[string]string{
		"Partyline": "Blue", "Blue": "Mono", "Mono": "Paperwhite", "Paperwhite": "Partyline",
		"Midnight": "Blue", "Raging Unicorn 🦄": "Blue", "Cotton Sky": "Mono", "": "Blue",
	} {
		if got := nextPalette(cur); got != next {
			t.Errorf("after %q: %q, want %q", cur, got, next)
		}
	}
}

func TestBackgroundRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if bg := loadBackground(); bg.Choice != "auto" || !bg.dark() {
		t.Errorf("default: %+v", bg)
	}
	_ = saveBackground(themeBackground{Choice: "auto", Detected: "light"})
	if bg := loadBackground(); bg.Detected != "light" || bg.dark() {
		t.Errorf("saved light: %+v", bg)
	}
}

// Every palette and variant keeps the conf's key bindings.
func TestTmuxConfBindingsUnchangedInEveryTheme(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want, err := os.ReadFile("testdata/tmux-bindings.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range uitheme.All() {
		_ = os.MkdirAll(stateDir(), 0o700)
		_ = os.WriteFile(themePath(), []byte(th.Palette+"\n"), 0o600)
		_ = saveBackground(themeBackground{Choice: map[bool]string{true: "dark", false: "light"}[th.Dark]})
		if got := chosenTheme(); got.Palette != th.Palette || got.Dark != th.Dark {
			t.Fatalf("chose %s/%v for %s/%v", got.Palette, got.Dark, th.Palette, th.Dark)
		}
		if got := confBindings("CLIPBOARD"); got != string(want) {
			t.Errorf("%s dark=%v changes the bindings", th.Palette, th.Dark)
		}
		if conf := tmuxConf(); !strings.Contains(conf, th.Focus) {
			t.Errorf("%s dark=%v: the conf does not use its focus colour %s", th.Palette, th.Dark, th.Focus)
		}
	}
}
