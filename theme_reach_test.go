package main

import (
	"charm.land/lipgloss/v2"
	uitheme "partyline.sh/partyline/internal/theme"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if name == "Blue" || name == "Paperwhite" {
		_ = saveBackground(themeBackground{Choice: "light"})
	}
}

// The menu printed a hardcoded truecolor amber that no theme could reach. Its hotkeys and its
// background now come from the chosen theme.
func TestMenuAccentFollowsTheTheme(t *testing.T) {
	useTheme(t, "Blue")
	blue := tmuxTheme()
	m := newMenuModel([]tmuxMenuItem{{label: "new session", key: "n"}}, "", menuActions{})
	if blue.Palette != "Blue" || m.View().BackgroundColor != lipgloss.Color(blue.Surface) {
		t.Errorf("Blue light: theme %s, menu background %v, want Blue's %s", blue.Palette, m.View().BackgroundColor, blue.Surface)
	}
	useTheme(t, "Partyline")
	if got := m.View().BackgroundColor; got != lipgloss.Color(uitheme.Default().Surface) {
		t.Errorf("Partyline dark: menu background %v, want Partyline dark's", got)
	}
}

// tmux draws the ribbon and popup borders itself, from the generated conf, so the theme has to be
// resolved when the conf is written. A light launcher theme gets its palette's light variant: a
// person on a white terminal must not get a dark bar (UI plan §3).
func TestGeneratedTmuxConfCarriesTheTheme(t *testing.T) {
	for name, palette := range map[string]string{"Blue": "Blue", "Paperwhite": "Paperwhite", "Partyline": "Partyline"} {
		useTheme(t, name)
		_ = saveBackground(themeBackground{Choice: "light"})
		want, _ := uitheme.Lookup(palette, false)
		if conf := tmuxConf(); !strings.Contains(conf, "bg="+want.Surface) || strings.Contains(conf, uitheme.Default().Surface) {
			t.Errorf("%s: the conf is not in %s light:\n%s", name, palette, firstLineOf(conf))
		}
	}
	useTheme(t, "Partyline")
	conf := tmuxConf()
	for _, want := range []string{"bg=" + uitheme.Default().Surface, "fg=" + uitheme.Default().Focus, uitheme.Default().Gradient[0]} {
		if !strings.Contains(conf, want) {
			t.Errorf("Partyline dark: the conf lacks the dark theme's %s", want)
		}
	}
}

// The menu's hotkey accent and selected row come from the chosen theme, light and dark: the colours
// themselves, which the plain-text goldens do not record.
func TestMenuColoursFollowTheTheme(t *testing.T) {
	sgr := func(hex string) string {
		var r, g, b int
		_, _ = fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b)
		return fmt.Sprintf("2;%d;%d;%d", r, g, b)
	}
	for _, bg := range []string{"dark", "light"} {
		name := "Partyline"
		useTheme(t, name)
		_ = saveBackground(themeBackground{Choice: bg})
		th := tmuxTheme()
		items := []tmuxMenuItem{{label: "new session", key: "n"}, {label: "shell", key: "t"}}
		out := newMenuModel(items, "", menuActions{}).View().Content
		if !strings.Contains(out, "48;"+sgr(th.Raised)) {
			t.Errorf("%s: the selected row is not on the theme's raised %s", name, th.Raised)
		}
		if !strings.Contains(out, "38;"+sgr(th.Focus)) {
			t.Errorf("%s: the hotkeys are not in the theme's focus %s", name, th.Focus)
		}
	}
}
