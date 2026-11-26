package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestLauncherActionsGolden records shared modal screens in both theme variants.
func TestLauncherActionsGolden(t *testing.T) {
	for _, name := range []string{"Midnight", "Daylight"} {
		for _, screen := range []string{"picker", "delete", "help"} {
			t.Run(name+"/"+screen, func(t *testing.T) {
				m := launcherActionFixture(t)
				useTheme(t, name)
				for i := range m.menu.all {
					m.menu.all[i].Cwd = "/projects/alpha"
				}
				m.menu.collapsed = map[string]bool{"/projects/alpha": false}
				m.menu.applyFilter()
				m.menu.cursor = 1
				switch screen {
				case "picker":
					m.menu.picking = true
					m.menu.pickModes = permissionModes("claude")
				case "delete":
					cp := *m.menu.selected()
					m.menu.confirmDelete = &cp
					m.menu.delFacts = deleteFacts{Branch: "main", Dirty: 3, Age: "2h", Recent: true, TrashUnder: "~/.partyline/trash"}
				case "help":
					m.menu.help = true
				}
				raw := m.View().Content
				got := gateANSI.ReplaceAllString(raw, "")
				got = launcherGoldenClock.ReplaceAllStringFunc(got, func(s string) string { return strings.Repeat("·", len([]rune(s))) })
				path := fmt.Sprintf("testdata/launcher-%s-80x24-%s.golden", screen, strings.ToLower(name))
				if *updateLoopsTab {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Fatalf("%s changed:\n%s", path, got)
				}
				if strings.Count(got, "\n")+1 > 24 {
					t.Fatal("screen exceeds popup")
				}
				th := tmuxTheme()
				for _, colour := range []string{th.Text, th.Focus} {
					var r, g, b int
					fmt.Sscanf(strings.TrimPrefix(colour, "#"), "%02x%02x%02x", &r, &g, &b)
					if !strings.Contains(raw, fmt.Sprintf("38;2;%d;%d;%d", r, g, b)) {
						t.Fatalf("missing %s", colour)
					}
				}
			})
		}
	}
}
