package main

import (
	"bufio"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// popupPress feeds named keys to a form without running a terminal program.
func popupPress(model tea.Model, keys ...string) (formModel, tea.Cmd) {
	m := formState(model)
	var cmd tea.Cmd
	for _, k := range keys {
		named := map[string]tea.KeyPressMsg{
			"up": {Code: tea.KeyUp}, "down": {Code: tea.KeyDown}, "enter": {Code: tea.KeyEnter}, "esc": {Code: tea.KeyEscape},
			"backspace": {Code: tea.KeyBackspace}, "ctrl+u": {Code: 'u', Mod: tea.ModCtrl}, "ctrl+c": {Code: 'c', Mod: tea.ModCtrl}, "ctrl+\\": {Code: '\\', Mod: tea.ModCtrl},
		}
		msg, ok := named[k]
		if !ok {
			msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		}
		next, c := model.Update(msg)
		model = next
		m, cmd = formState(next), c
	}
	return m, cmd
}

// popupChoices checks every action and cancel key for a one-key popup.
func popupChoices(t *testing.T, initial tea.Model) {
	t.Helper()
	for _, row := range formState(initial).rows {
		for _, k := range []string{row.key, strings.ToUpper(row.key)} {
			t.Run(k, func(t *testing.T) {
				m, c := popupPress(initial, k)
				if !m.ok || m.key != []rune(row.key)[0] || c == nil {
					t.Fatalf("%s: key=%q ok=%v cmd=%v", k, m.key, m.ok, c != nil)
				}
			})
		}
	}
	for _, k := range []string{"q", "Q", "esc", "enter", "ctrl+c", "ctrl+\\"} {
		t.Run(k, func(t *testing.T) {
			m, c := popupPress(initial, k)
			if m.ok || m.key != 0 || c == nil {
				t.Fatalf("%s did not cancel", k)
			}
		})
	}
	for _, k := range []string{"up", "down", "z"} {
		m, c := popupPress(initial, k)
		if m.ok || c != nil {
			t.Errorf("%s acted", k)
		}
	}
}

// popupProgram replaces terminal I/O with direct model updates.
func popupProgram(t *testing.T, fn func(formModel) formModel) {
	t.Helper()
	oldTTY, oldRun := formTerminal, formProgram
	formTerminal = func() bool { return true }
	formProgram = func(m tea.Model) (tea.Model, error) { return fn(formState(m)), nil }
	t.Cleanup(func() { formTerminal, formProgram = oldTTY, oldRun })
}

// popupStdin puts line on a piped stdin with no terminal, behind the shared buffered reader.
func popupStdin(t *testing.T, line string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.WriteString(line); err != nil {
		t.Fatal(err)
	}
	w.Close()
	stdin()
	oldIn, oldBuf, oldTTY := os.Stdin, stdinBuf, formTerminal
	os.Stdin = r
	stdinBuf = bufio.NewReader(r)
	formTerminal = func() bool { return false }
	t.Cleanup(func() { r.Close(); os.Stdin, stdinBuf, formTerminal = oldIn, oldBuf, oldTTY })
}
