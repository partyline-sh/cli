package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// theme_reach_test.go — the theme is a property of the INSTALL, not of the launcher.
//
// It was loaded only on the launcher's boot path, so `ptln board` and the ctrl-\ menu rendered
// in the default palette however the user had set theirs. The board even called themed() on
// every frame — against a table that had never been loaded, so the call did nothing at all.

// useTheme picks a theme the way a user would, in an isolated HOME.
func useTheme(t *testing.T, name string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(themePath(), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, savedIdx := theme, themeIdx
	t.Cleanup(func() { theme, themeIdx = saved, savedIdx })
	loadTheme()
	if theme.name != name {
		t.Fatalf("theme = %q, want %q", theme.name, name)
	}
}

func TestThemeLoadsForEverySurfaceNotJustTheLauncher(t *testing.T) {
	useTheme(t, "Cotton Sky")
	// 215 is the accent every surface uses for a pressable key; Cotton Sky remaps it to 39.
	if got := ThemedIndex(accentIdx); got != "39" {
		t.Fatalf("the accent did not follow the theme: %q", got)
	}
	if got := ThemedIndex("nosuch"); got != "nosuch" {
		t.Errorf("an unmapped index must pass through, got %q", got)
	}
	useTheme(t, "Midnight")
	if got := ThemedIndex(accentIdx); got != accentIdx {
		t.Errorf("Midnight is the identity theme, got %q", got)
	}
}

// The menu printed a hardcoded truecolor amber. themed() only rewrites ANSI-256 indexes, so no
// theme could ever reach it — the menu stayed brand-amber beside a launcher that had changed.
func TestMenuAccentFollowsTheTheme(t *testing.T) {
	useTheme(t, "Cotton Sky")
	out := renderTmuxMenu([]tmuxMenuItem{{label: "new session", key: "n"}}, -1, -1, 0)
	if strings.Contains(out, "38;2;") {
		t.Errorf("the menu is still painting truecolor, which no theme can remap:\n%q", out)
	}
	if !strings.Contains(out, "38;5;39m") {
		t.Errorf("the menu's accent is not the theme's:\n%q", out)
	}

	useTheme(t, "Midnight")
	if !strings.Contains(renderTmuxMenu([]tmuxMenuItem{{label: "x", key: "n"}}, -1, -1, 0), "38;5;215m") {
		t.Error("Midnight should leave the accent at its source index")
	}
}

// tmux draws the ribbon and popup borders itself, from the generated conf — nothing Go writes
// passes through themed() on the way, so the colour has to be resolved when the conf is written.
func TestGeneratedTmuxConfCarriesTheTheme(t *testing.T) {
	useTheme(t, "Cotton Sky")
	if conf := tmuxConf(); !strings.Contains(conf, "colour39") {
		t.Errorf("the conf did not take the theme's accent:\n%s", firstLineOf(conf))
	}
	useTheme(t, "Paperwhite")
	if !strings.Contains(tmuxConf(), "colour"+ThemedIndex(accentIdx)) {
		t.Error("a second theme did not reach the conf")
	}

	// The DEFAULT look must not drift because a themable path was added: colour215 is only near
	// the brand amber, and Midnight is meant to be the original, unchanged.
	useTheme(t, "Midnight")
	if !strings.Contains(tmuxConf(), brand.Hex(brand.AmberRGB)) {
		t.Error("the identity theme lost the exact brand hex")
	}
}

// The board renders through themed(); with the theme loaded, its accent must move too.
func TestBoardFrameIsThemed(t *testing.T) {
	useTheme(t, "Cotton Sky")
	// boardCol is 39 at source, which Cotton Sky leaves alone; 215 is what proves the pipe works.
	if themed("\x1b[38;5;215mkey\x1b[0m") == "\x1b[38;5;215mkey\x1b[0m" {
		t.Fatal("themed() did not rewrite an index the active theme remaps")
	}
	useTheme(t, "Midnight")
	if got := themed("\x1b[38;5;215mkey\x1b[0m"); got != "\x1b[38;5;215mkey\x1b[0m" {
		t.Errorf("Midnight must not rewrite anything, got %q", got)
	}
}

// The wordmark's gradient was truecolor, which themed() cannot rewrite — so the banner stayed
// amber-and-pink under every theme, even though each theme table carries explicit "banner
// gradient" remaps for exactly those stops.
func TestWordmarkFallsBackToIndexedUnderATheme(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")

	useTheme(t, "Midnight") // identity — full colour fidelity is the point
	if !strings.Contains(brand.Wordmark(), "38;2;") {
		t.Error("the identity theme should keep truecolor")
	}

	useTheme(t, "Cotton Sky")
	mark := brand.Wordmark()
	if strings.Contains(mark, "38;2;") {
		t.Fatal("under a theme the wordmark must render indexed, or the remap cannot reach it")
	}
	if themed(mark) == mark {
		t.Error("the themed wordmark is identical to the raw one — no remap landed")
	}
}
