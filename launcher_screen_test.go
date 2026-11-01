package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// launcherKey constructs key messages without starting a program.
func launcherKey(k string) tea.KeyPressMsg {
	switch k {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+\\":
		return tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

// launcherGoldens checks bounds, theme colours and the six screen fixtures.
func launcherGoldens(t *testing.T, name string, model tea.Model) {
	t.Helper()
	for _, theme := range []struct{ name, label string }{{"Midnight", "dark"}, {"Daylight", "light"}} {
		t.Run(theme.label, func(t *testing.T) {
			useTheme(t, theme.name)
			for _, size := range [][2]int{{80, 24}, {120, 38}, {60, 20}} {
				m, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				view := m.View()
				if !view.AltScreen {
					t.Fatal("screen does not use alternate screen")
				}
				var r, g, b int
				fmt.Sscanf(strings.TrimPrefix(tmuxTheme().Focus, "#"), "%02x%02x%02x", &r, &g, &b)
				if !strings.Contains(view.Content, fmt.Sprintf("38;2;%d;%d;%d", r, g, b)) {
					t.Fatal("missing theme Focus foreground")
				}
				got := ansix.Strip(view.Content)
				if len(strings.Split(got, "\n")) > size[1] {
					t.Fatal("screen too tall")
				}
				for _, line := range strings.Split(got, "\n") {
					if ansix.StringWidth(line) > size[0] {
						t.Fatal("screen too wide")
					}
				}
				path := fmt.Sprintf("testdata/%s-%dx%d-%s.golden", name, size[0], size[1], theme.label)
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
					t.Errorf("%s changed", path)
				}
			}
		})
	}
}

// TestWelcome pins navigation, shortcuts, exit destinations and ignored keys.
func TestWelcome(t *testing.T) {
	doors := []welcomeDoor{{disp: "↵", key: '\r', title: "resume last session"}, {disp: "n", key: 'n', title: "new session"}, {disp: "/", key: '/', title: "find a session"}}
	for _, canSwitch := range []bool{false, true} {
		for _, tc := range []struct {
			k           string
			sel, result int
			quit        bool
		}{
			{"up", 2, -1, false}, {"down", 1, -1, false}, {"j", 1, -1, false}, {"k", 2, -1, false},
			{"enter", 0, 0, true}, {"n", 0, 1, true}, {"/", 0, 2, true},
			{"q", 0, -2, true}, {"esc", 0, -2, true}, {"ctrl+c", 0, -2, true},
			{"ctrl+\\", 0, -1, false}, {"N", 0, -1, false}, {"left", 0, -1, false},
		} {
			t.Run(fmt.Sprintf("%t/%s", canSwitch, tc.k), func(t *testing.T) {
				m := welcomeModel{doors: doors, canSwitch: canSwitch, result: -1}
				next, cmd := m.Update(launcherKey(tc.k))
				got := next.(welcomeModel)
				want := tc.result
				if want == -2 {
					want = -1
					if canSwitch {
						want = len(doors)
					}
				}
				if got.sel != tc.sel || got.result != want || (cmd != nil) != tc.quit {
					t.Fatalf("%s: %+v quit=%t", tc.k, got, cmd != nil)
				}
			})
		}
	}
	launcherGoldens(t, "welcome", welcomeModel{doors: doors, result: -1})
}

// choiceKeys checks the legacy menu's action and cancellation table.
func choiceKeys(t *testing.T, m formModel, keys string) {
	t.Helper()
	for _, k := range append(strings.Fields(keys), "q", "Q", "esc", "enter", "ctrl+c", "ctrl+\\", "z", "up", "down", "left") {
		t.Run(k, func(t *testing.T) {
			next, cmd := m.Update(launcherKey(k))
			got := next.(formModel)
			want := rune(0)
			for _, row := range m.rows {
				if strings.ToLower(k) == row.key && row.key != "q" {
					want = []rune(row.key)[0]
				}
			}
			ignored := k == "up" || k == "down" || k == "left"
			if got.key != want || got.ok != (want != 0) || (cmd != nil) == ignored {
				t.Fatalf("key=%q action=%q ok=%t quit=%t", k, got.key, got.ok, cmd != nil)
			}
		})
	}
}

// TestEmptyState pins every adoption key and cancellation.
func TestEmptyState(t *testing.T) {
	t.Setenv("HOME", "/home/partyline")
	found := []string{"/home/old", "/home/two", "/home/three", "/home/four", "/home/five", "/home/six", "/home/seven", "/home/eight", "/home/nine"}
	choiceKeys(t, emptyStateForm(found), "1 2 3 4 5 6 7 8 9 p P")
	launcherGoldens(t, "empty-state", emptyStateForm(found[:1]))
}

// TestRecover pins each recovery action, including the absent recorded remote.
func TestRecover(t *testing.T) {
	choiceKeys(t, recoverForm("/work/gone", "https://example.org/repo"), "1 d D r R e E")
	choiceKeys(t, recoverForm("/work/gone", ""), "1 d r e")
	launcherGoldens(t, "recover", recoverForm("/work/gone", "https://example.org/repo"))
}

// TestBootSplashScreen pins the passive splash's key table.
func TestBootSplashScreen(t *testing.T) {
	m := bootScreen{model: bootModel{Elapsed: bootThreshold, Total: 4, Steps: []bootStep{{State: bootDone}}}, w: 80, h: 24}
	for _, k := range []string{"enter", "esc", "q", "ctrl+c", "ctrl+\\", "up", "down", "j", "k", "n", "/"} {
		t.Run(k, func(t *testing.T) {
			next, cmd := m.Update(launcherKey(k))
			got := next.(bootScreen)
			if cmd != nil || got.model.doneCount() != 1 {
				t.Fatal("key changed boot progress")
			}
		})
	}
	launcherGoldens(t, "boot-splash", m)
}

// TestEmptyStateNotesKeepMenuKeySemantics pins message dismissal, including ignored sequences.
func TestEmptyStateNotesKeepMenuKeySemantics(t *testing.T) {
	m := formModel{kind: formNote, cancelUnknown: true}
	for _, k := range []string{"up", "down", "left", "q", "enter", "esc", "ctrl+c", "ctrl+\\"} {
		_, cmd := m.Update(launcherKey(k))
		ignored := k == "up" || k == "down" || k == "left"
		if (cmd != nil) == ignored {
			t.Errorf("%s: quit=%t", k, cmd != nil)
		}
	}
}

// TestRecoverFields keeps the shared field keys exercised through Update.
func TestRecoverFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		keys   []tea.KeyPressMsg
		answer string
		ok     bool
	}{
		{"enter", []tea.KeyPressMsg{launcherKey("enter")}, "", false},
		{"esc", []tea.KeyPressMsg{launcherKey("esc")}, "", false},
		{"ctrl-c", []tea.KeyPressMsg{launcherKey("ctrl+c")}, "", false},
		{"ctrl-backslash", []tea.KeyPressMsg{launcherKey("ctrl+\\")}, "", false},
		{"lone-q", []tea.KeyPressMsg{launcherKey("q"), launcherKey("enter")}, "", false},
		{"backspace", []tea.KeyPressMsg{launcherKey("d"), launcherKey("x"), {Code: tea.KeyBackspace}, launcherKey("enter")}, "d", true},
		{"ctrl-u", []tea.KeyPressMsg{launcherKey("x"), {Code: 'u', Mod: tea.ModCtrl}, launcherKey("d"), launcherKey("enter")}, "d", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m tea.Model = formModel{kind: formAsk}
			for _, key := range tc.keys {
				m, _ = m.Update(key)
			}
			got := m.(formModel)
			if got.answer != tc.answer || got.ok != tc.ok {
				t.Fatalf("answer=%q ok=%t", got.answer, got.ok)
			}
		})
	}
}

// TestWelcomeEnterUsesSelection checks Enter with and without a resume door.
func TestWelcomeEnterUsesSelection(t *testing.T) {
	for _, doors := range [][]welcomeDoor{
		{{disp: "n", key: 'n', title: "new session"}},
		{{disp: "↵", key: '\r', title: "resume last session"}, {disp: "n", key: 'n', title: "new session"}},
	} {
		for i := range doors {
			m := welcomeModel{doors: doors, sel: i, result: -1}
			next, cmd := m.Update(launcherKey("enter"))
			if next.(welcomeModel).result != i || cmd == nil {
				t.Fatal("Enter did not open the selected door")
			}
		}
	}
}

// headlessOutput captures output with file-backed stdin and stdout.
func headlessOutput(t *testing.T, fn func()) string {
	t.Helper()
	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	fn()
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestWelcomeWithoutATerminal keeps the CLI diagnostic and the loop's raw-mode failure.
func TestWelcomeWithoutATerminal(t *testing.T) {
	got := headlessOutput(t, func() {
		welcomeMain(nil)
		idx, err := welcomeLoop([]welcomeDoor{{key: 'n'}}, false)
		if idx != -1 || err == nil {
			t.Errorf("nonterminal loop returned %d, %v", idx, err)
		}
	})
	if got != "ptln welcome needs an interactive terminal\n" {
		t.Fatalf("output=%q", got)
	}
}

// TestEmptyStateWithoutATerminal keeps the legacy frame and declines adoption.
func TestEmptyStateWithoutATerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := headlessOutput(t, func() {
		cgBox("Where should I look?", emptyStateLines(candidateRoots(), emptyChoices(candidateRoots())))
	})
	got := headlessOutput(t, func() {
		if offerSessionRoots() {
			t.Error("headless launch adopted a root")
		}
	})
	if got != want {
		t.Fatal("headless empty-state output changed")
	}
	for _, title := range []string{"Added", "Couldn't save that"} {
		lines := []string{"message"}
		want = headlessOutput(t, func() { cgBox(title, lines) })
		got = headlessOutput(t, func() { emptyStateNote(title, lines) })
		if got != want {
			t.Fatalf("%s headless output changed", title)
		}
	}
}

// TestRecoverWithoutATerminal keeps the painted frame and performs no recovery.
func TestRecoverWithoutATerminal(t *testing.T) {
	dir := t.TempDir() + "/gone"
	got := headlessOutput(t, func() { recoverModal(aiSession{Cwd: dir}, "")() })
	plain := ansix.Strip(got)
	if !strings.Contains(plain, "Recover session") || !strings.Contains(plain, "its working directory was removed:") {
		t.Fatal("headless recovery frame missing")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("headless recovery created directory: %v", err)
	}
	// With no terminal the note prints its frame — title, message and the back hint — and returns.
	got = ansix.Strip(headlessOutput(t, func() { recoveryNote("Recover session", []string{"message"}) }))
	for _, want := range []string{"Recover session", "message", "back"} {
		if !strings.Contains(got, want) {
			t.Fatalf("headless recovery note lost %q: %q", want, got)
		}
	}
}

// ctrl with an arrow or function key is an escape sequence the old key reader ignored: it does not
// close Recover or the empty state. ctrl with a letter still closes them.
func TestRecoverIgnoresCtrlArrows(t *testing.T) {
	m := recoverForm("/gone", "")
	for _, code := range []rune{tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyF5} {
		if _, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl}); cmd != nil {
			t.Errorf("ctrl+%v closed Recover", code)
		}
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); cmd == nil {
		t.Error("ctrl+x did not close Recover")
	}
}

// Nine detected homes at 60x20: the body gives way so the last home and the p / q rows still show.
func TestEmptyStateKeepsEveryRowOnAShortScreen(t *testing.T) {
	var found []string
	for i := 1; i <= 9; i++ {
		found = append(found, fmt.Sprintf("/Users/other%d", i))
	}
	m := emptyStateForm(found)
	m.w, m.h = 60, 20
	out := gateANSI.ReplaceAllString(m.View().Content, "")
	for _, want := range []string{"/Users/other9", "type a path", "not now"} {
		if !strings.Contains(out, want) {
			t.Errorf("60x20 with nine homes lost %q:\n%s", want, out)
		}
	}
}
