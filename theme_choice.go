package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	uitheme "partyline.sh/partyline/internal/theme"
)

// The theme a person picks: a palette name, kept in themePath, and a background setting
// with the value detected for auto (docs/plans/bubbletea-ui.md §3).

type themeBackground struct {
	Choice   string // auto, dark or light
	Detected string // dark, light or none (it did not answer), from the outer terminal; "" before it is asked
}

func themeBgPath() string { return filepath.Join(stateDir(), "theme-background") }

func loadBackground() themeBackground {
	b, _ := os.ReadFile(themeBgPath())
	f := strings.Fields(string(b))
	bg := themeBackground{Choice: "auto"}
	if len(f) > 0 && (f[0] == "dark" || f[0] == "light" || f[0] == "auto") {
		bg.Choice = f[0]
	}
	if len(f) > 1 && (f[1] == "dark" || f[1] == "light" || f[1] == "none") {
		bg.Detected = f[1]
	}
	return bg
}

func saveBackground(bg themeBackground) error {
	_ = os.MkdirAll(stateDir(), 0o700)
	return os.WriteFile(themeBgPath(), []byte(strings.TrimSpace(bg.Choice+" "+bg.Detected)+"\n"), 0o600)
}

// dark: the variant to draw. A terminal that never answered is taken as dark.
func (bg themeBackground) dark() bool {
	switch bg.Choice {
	case "dark":
		return true
	case "light":
		return false
	}
	return bg.Detected != "light"
}

// legacyThemes are the names saved before palettes: each pins a palette and a variant, whatever
// the background setting. savedThemeName migrates these on read.
var legacyThemes = map[string]struct {
	palette string
	dark    bool
}{
	"Midnight":         {"Partyline", true},
	"Raging Unicorn":   {"Partyline", false},
	"Daylight":         {"Partyline", false},
	"Raging Unicorn 🦄": {"Partyline", false}, // folded into Partyline light (the user's decision)
	"Cotton Sky":       {"Blue", false},
	"Paperwhite":       {"Paperwhite", false},
}

func savedThemeName() string {
	b, _ := os.ReadFile(themePath())
	name := strings.TrimSpace(string(b))
	if lt, ok := legacyThemes[name]; ok && name != "Paperwhite" {
		bg := loadBackground()
		bg.Choice = "light"
		if lt.dark {
			bg.Choice = "dark"
		}
		if saveBackground(bg) == nil {
			if os.WriteFile(themePath(), []byte(lt.palette+"\n"), 0o600) == nil {
				return lt.palette
			}
		}
	}
	return name
}

// themeFor is the theme a saved name and background setting give.
func themeFor(name string, bg themeBackground) uitheme.Theme {
	if lt, ok := legacyThemes[name]; ok {
		t, _ := uitheme.Lookup(lt.palette, lt.dark)
		return t
	}
	if t, ok := uitheme.Lookup(name, bg.dark()); ok {
		return t
	}
	t, _ := uitheme.Lookup("Partyline", bg.dark())
	return t
}

func chosenTheme() uitheme.Theme { return themeFor(savedThemeName(), loadBackground()) }

// nextPalette is the palette after the saved one in the launcher's cycle; a legacy name continues
// from the palette it maps to.
func nextPalette(name string) string {
	cur := themeFor(name, loadBackground()).Palette
	for i, p := range uitheme.Palettes {
		if p == cur {
			return uitheme.Palettes[(i+1)%len(uitheme.Palettes)]
		}
	}
	return uitheme.Palettes[0]
}

// applyChosenTheme rewrites the tmux conf for the theme and has a running server read it.
func applyChosenTheme() {
	if writeTmuxConf() == nil {
		_ = tmuxCmd("source-file", filepath.Join(stateDir(), "tmux.conf")).Run()
	}
}

// themeMain is `ptln theme [name] [--background auto|dark|light]`.
func themeMain(args []string) {
	bg := loadBackground()
	var name string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--background" && i+1 < len(args):
			i++
			if v := args[i]; v == "auto" || v == "dark" || v == "light" {
				bg.Choice = v
			} else {
				fatal(fmt.Errorf("--background is auto, dark or light, not %q", v))
			}
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("usage: ptln theme [%s] [--background auto|dark|light]", strings.Join(uitheme.Palettes, "|")))
		default:
			t, ok := uitheme.Lookup(a, true)
			if !ok {
				fatal(fmt.Errorf("no theme %q; the themes are %s", a, strings.Join(uitheme.Palettes, ", ")))
			}
			name = t.Palette
		}
	}
	was := loadBackground()
	if bg.Choice == "auto" && !insidePtlnTmux() {
		bg.Detected = detectBackground(bg.Detected)
	}
	// A query changes nothing on disk or on screen unless the terminal's answer changed.
	if bg != was {
		if err := saveBackground(bg); err != nil {
			fatal(err)
		}
	}
	if name != "" {
		_ = os.MkdirAll(stateDir(), 0o700)
		if err := os.WriteFile(themePath(), []byte(name+"\n"), 0o600); err != nil {
			fatal(err)
		}
	}
	if bg != was || name != "" {
		applyChosenTheme()
	}
	t := chosenTheme()
	variant := map[bool]string{true: "dark", false: "light"}[t.Dark]
	fmt.Printf("theme %s, %s · background %s", t.Palette, variant, bg.Choice)
	if bg.Choice == "auto" {
		detected := bg.Detected
		switch detected {
		case "":
			detected = "not asked yet, so dark"
		case "none":
			detected = "no answer, so dark"
		}
		fmt.Printf(" (detected: %s)", detected)
	}
	if _, legacy := legacyThemes[savedThemeName()]; legacy {
		fmt.Printf(" · saved as %q, which pins %s", savedThemeName(), variant)
	}
	fmt.Printf("\nthemes: %s\n", strings.Join(uitheme.Palettes, ", "))
}
