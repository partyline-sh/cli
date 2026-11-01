package main

import (
	"os"
	"strings"
	"testing"
)

func TestThemeNameMigration(t *testing.T) {
	for name, want := range map[string]struct{ name, bg string }{
		"Midnight": {"Partyline", "dark"}, "Daylight": {"Partyline", "light"},
		"Raging Unicorn": {"Partyline", "light"}, "Raging Unicorn 🦄": {"Partyline", "light"},
		"Cotton Sky": {"Blue", "light"}, "Paperwhite": {"Paperwhite", "auto"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if err := os.MkdirAll(stateDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(themePath(), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := savedThemeName(); got != want.name {
				t.Fatalf("name %q", got)
			}
			if got := loadBackground().Choice; got != want.bg {
				t.Fatalf("background %q", got)
			}
			b, _ := os.ReadFile(themePath())
			if strings.TrimSpace(string(b)) != want.name {
				t.Fatalf("persisted %q", b)
			}
			if got := savedThemeName(); got != want.name {
				t.Fatal("migration not stable")
			}
		})
	}
}
