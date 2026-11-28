package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// TestSessionPopups pins themed popup screens and their terminal bounds.
func TestSessionPopups(t *testing.T) {
	forms := map[string]tea.Model{
		"new-run": newRunForm(), "worktree": worktreeForm(),
		"ask":        formModel{kind: formAsk, title: "Fork into worktree", body: []string{"an isolated checkout — same history, own dir, own branch"}, label: "worktree name", def: "feature"},
		"confirm":    formModel{kind: formConfirm, title: "Remove a worktree", body: []string{"the directory goes away; the branch survives"}, label: "delete the worktree directory?"},
		"pick":       formModel{kind: formPick, title: "Remove a worktree", items: []string{"⎇ feature", "⎇ fix"}, label: "remove"},
		"note":       formModel{kind: formNote, title: "Worktrees", body: []string{"⎇ feature  /workspace/feature", "⎇ fix      /workspace/fix"}},
		"keep-going": formModel{kind: formAsk, title: "Keep-going", label: "continuations to arm (0 or off to disarm)"},
	}
	forms["pick-long"] = formModel{kind: formPick, title: "Switch thread", label: "attach", items: strings.Split(strings.Repeat("thread,", 29)+"thread", ","), extras: []formExtra{{Key: 'n', Label: "ask someone new"}}, off: 20}
	forms["choice"] = formModel{kind: formChoice, title: "Choose", rows: []formRow{{"a", "action", "optional note"}}}

	for _, theme := range []struct{ name, file string }{{"Midnight", "dark"}, {"Daylight", "light"}} {
		t.Run(theme.file, func(t *testing.T) {
			useTheme(t, theme.name)
			for name, m := range forms {
				t.Run(name, func(t *testing.T) {
					sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
					v := sized.View()
					got := gateANSI.ReplaceAllString(v.Content, "")
					if v.BackgroundColor == nil || fmt.Sprint(v.BackgroundColor) != fmt.Sprint(gateStyle{tmuxTheme()}.fg(tmuxTheme().Surface).GetForeground()) {
						t.Fatal("popup background does not follow the theme")
					}
					if strings.Count(got, "\n")+1 > 24 {
						t.Fatal("popup taller than terminal")
					}
					for _, line := range strings.Split(got, "\n") {
						if runewidth.StringWidth(line) > 80 {
							t.Fatalf("line too wide: %s", line)
						}
					}
					path := fmt.Sprintf("testdata/session-popup-%s-%s.golden", name, theme.file)
					if *updateLoopsTab {
						if err := os.WriteFile(path, []byte(got), 0644); err != nil {
							t.Fatal(err)
						}
					}
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if got != string(want) {
						t.Errorf("%s changed:\n%s", path, got)
					}
				})
			}
		})
	}
}

// TestSessionPopupsNewRun covers both doors and queues a shell only after its form exits.
func TestSessionPopupsNewRun(t *testing.T) {
	popupChoices(t, newRunForm())
	for _, key := range []string{"1", "2", "q", "esc", "enter", "ctrl+c", "ctrl+\\"} {
		t.Run("flow-"+key, func(t *testing.T) {
			target := &tmuxSessionTarget{}
			calls := 0
			popupProgram(t, func(m formModel) formModel {
				if target.pendingOpen != nil {
					if m.kind != formNote {
						t.Fatal("launch queued before input finished")
					}
					return m
				}
				calls++
				switch calls {
				case 1:
					m, _ = popupPress(m, key)
				default:
					if key == "2" {
						m, _ = popupPress(m, "enter")
					} else {
						m, _ = popupPress(m, "esc")
					}
				}
				return m
			})
			newRunMenu(target)
			if (target.pendingOpen != nil) != (key == "2") {
				t.Fatalf("key %s queued %#v", key, target.pendingOpen)
			}
			if key == "2" {
				dir, _ := os.Getwd()
				if target.pendingOpen.Dir != dir || len(target.pendingOpen.Argv) == 0 {
					t.Fatal("shell spec lost directory or argv")
				}
			}
		})
	}
}

// TestSessionPopupsWorktree checks each worktree door and all exits.
func TestSessionPopupsWorktree(t *testing.T) {
	popupChoices(t, worktreeForm())
	testSessionWorktreeFlows(t)
}

// TestSessionPopupsTarget checks dispatch for the captured session's popup bindings.
func TestSessionPopupsTarget(t *testing.T) {
	// "w" offers worktrees only inside a git repository, and the target has no session dir, so
	// the menu reads the working directory: make it a repository rather than rely on where the
	// tests run from.
	t.Chdir(initRepo(t))
	for _, tc := range []struct {
		key   string
		kind  formKind
		title string
	}{{"n", formChoice, "New / Run"}, {"N", formPick, "engine"}, {"w", formChoice, "Worktrees"}, {"g", formNote, "Keep-going"}} {
		t.Run(tc.key, func(t *testing.T) {
			seen := 0
			popupProgram(t, func(m formModel) formModel {
				seen++
				if m.kind != tc.kind || m.title != tc.title {
					t.Fatalf("%s opened kind %d %s", tc.key, m.kind, m.title)
				}
				m, _ = popupPress(m, "esc")
				return m
			})
			sessionTargetMenu(&tmuxSessionTarget{}, tc.key)
			if seen != 1 {
				t.Fatalf("opened %d forms", seen)
			}
		})
	}
}

// TestSessionPopupsModals covers each form's input contract.
func TestSessionPopupsModals(t *testing.T) {
	popupChoices(t, newRunForm())
	for _, k := range []string{"a", "enter", "up", "esc", "q", "ctrl+c", "ctrl+\\"} {
		t.Run("note-"+k, func(t *testing.T) {
			_, c := popupPress(formModel{kind: formNote}, k)
			if c == nil {
				t.Fatal("note stayed open")
			}
		})
	}
	for _, def := range []bool{false, true} {
		for _, k := range []string{"y", "Y", "n", "N", "enter", "q", "Q", "esc", "ctrl+c", "ctrl+\\", "up", "z"} {
			t.Run(fmt.Sprintf("confirm-%v-%s", def, k), func(t *testing.T) {
				m, c := popupPress(formModel{kind: formConfirm, defaultYes: def}, k)
				ok := strings.EqualFold(k, "y") || strings.EqualFold(k, "n") || k == "enter"
				value := strings.EqualFold(k, "y") || k == "enter" && def
				if m.ok != ok || m.value != (ok && value) || (c == nil) != (k == "up" || k == "z") {
					t.Fatalf("got (%v,%v), cmd=%v", m.value, m.ok, c != nil)
				}
			})
		}
	}
	for _, tc := range []struct {
		keys      []string
		def, want string
		ok        bool
	}{
		{[]string{"enter"}, "fallback", "fallback", true}, {[]string{"enter"}, "", "", false}, {[]string{"Q", "enter"}, "fallback", "", false},
		{[]string{"q", "x", "enter"}, "", "qx", true}, {[]string{"é", "backspace", "x", "enter"}, "", "x", true}, {[]string{"abc", "ctrl+u", "enter"}, "fallback", "fallback", true},
		{[]string{"esc"}, "fallback", "", false}, {[]string{"ctrl+c"}, "", "", false}, {[]string{"ctrl+\\"}, "", "", false},
	} {
		t.Run("ask-"+strings.Join(tc.keys, "-"), func(t *testing.T) {
			m, c := popupPress(formModel{kind: formAsk, def: tc.def}, tc.keys...)
			if m.answer != tc.want || m.ok != tc.ok || c == nil {
				t.Fatalf("got (%q,%v)", m.answer, m.ok)
			}
		})
	}
	pasted, _ := (formModel{kind: formAsk}).Update(tea.PasteMsg{Content: "hello\nworld"})
	m, _ := popupPress(formState(pasted), "enter")
	if m.answer != "hello world" || !m.ok {
		t.Fatal("paste lost text or submitted early")
	}
	for _, tc := range []struct {
		keys         []string
		count, index int
		key          rune
		ok           bool
	}{
		{[]string{"1"}, 3, 0, 0, true}, {[]string{"2"}, 3, 1, 0, true}, {[]string{"3"}, 3, 2, 0, true}, {[]string{"9", "2"}, 3, 1, 0, true},
		{[]string{"0", "q"}, 3, 0, 0, false}, {[]string{"down", "enter"}, 3, 0, 0, false}, {[]string{"down", "up", "2"}, 3, 1, 0, true}, {[]string{"enter"}, 3, 0, 0, false},
		{[]string{"1", "2", "enter"}, 12, 11, 0, true}, {[]string{"9", "9", "enter", "7", "enter"}, 12, 6, 0, true}, {[]string{"1", "2", "backspace", "enter"}, 12, 0, 0, true}, {[]string{"down", "down", "1", "1", "enter"}, 12, 10, 0, true},
		{[]string{"n"}, 3, 0, 'n', true}, {[]string{"N"}, 3, 0, 'n', true}, {[]string{"esc"}, 3, 0, 0, false}, {[]string{"q"}, 3, 0, 0, false}, {[]string{"Q"}, 3, 0, 0, false}, {[]string{"ctrl+c"}, 3, 0, 0, false}, {[]string{"ctrl+\\"}, 3, 0, 0, false},
	} {
		t.Run("pick-"+strings.Join(tc.keys, "-"), func(t *testing.T) {
			m, c := popupPress(formModel{kind: formPick, items: make([]string, tc.count), extras: []formExtra{{Key: 'n', Label: "new"}}}, tc.keys...)
			if m.ok != tc.ok || m.key != tc.key || tc.ok && m.index != tc.index || c == nil {
				t.Fatalf("got index=%d key=%q ok=%v", m.index, m.key, m.ok)
			}
		})
	}
}

// TestSessionPopupsPrompts covers the terminal forms and stdin line readers.
func TestSessionPopupsPrompts(t *testing.T) { testSessionPopupPrompts(t) }

// ↓ past the bottom of a list does not wind up an offset that ↑ then has to unwind.
func TestSessionPopupsPickScrollIsBounded(t *testing.T) {
	m := formModel{kind: formPick, items: make([]string, 30), w: 80, h: 24}
	for i := 0; i < 40; i++ {
		m, _ = popupPress(m, "down")
	}
	if want := 30 - m.pickRows(); m.off != want {
		t.Fatalf("offset %d after 40 downs, want %d", m.off, want)
	}
	m, _ = popupPress(m, "up")
	if m.off != 30-m.pickRows()-1 {
		t.Errorf("one ↑ at the bottom did not scroll: offset %d", m.off)
	}
	short := formModel{kind: formPick, items: make([]string, 3), w: 80, h: 24}
	if short, _ = popupPress(short, "down"); short.off != 0 {
		t.Errorf("a list that fits scrolled to %d", short.off)
	}
}
