package main

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
)

// testPickerForm builds the three-item picker used by reader compatibility tests.
func testPickerForm() formModel {
	return formModel{kind: formPick, items: []string{"one", "two", "three"}, extras: []formExtra{{Key: 'n', Label: "ask someone new"}}}
}

// TestPickerSelectsOnASingleDigit pins the one-key selection and visible list.
func TestPickerSelectsOnASingleDigit(t *testing.T) {
	m, c := popupPress(testPickerForm(), "2")
	if !m.ok || m.index != 1 || m.key != 0 || c == nil {
		t.Fatalf("selection = %#v", m)
	}
	if !strings.Contains(gateANSI.ReplaceAllString(m.View().Content, ""), "two") {
		t.Fatal("list missing from form")
	}
}

// TestPickerAlwaysHasAWayOut checks every immediate cancel key.
func TestPickerAlwaysHasAWayOut(t *testing.T) {
	for _, k := range []string{"esc", "q", "Q", "ctrl+c", "ctrl+\\"} {
		t.Run(k, func(t *testing.T) {
			m, c := popupPress(testPickerForm(), k)
			if m.ok || c == nil {
				t.Fatal("picker did not cancel")
			}
		})
	}
}

// TestPickerIgnoresAnOutOfRangeDigit keeps an invalid digit from selecting or closing.
func TestPickerIgnoresAnOutOfRangeDigit(t *testing.T) {
	for _, k := range []string{"9", "0"} {
		t.Run(k, func(t *testing.T) {
			m, c := popupPress(testPickerForm(), k)
			if m.ok || c != nil {
				t.Fatal("invalid digit acted")
			}
			m, c = popupPress(m, "2")
			if !m.ok || m.index != 1 || c == nil {
				t.Fatal("could not retry")
			}
		})
	}
}

// TestPickerReturnsExtraKeys preserves the extra-key return tuple.
func TestPickerReturnsExtraKeys(t *testing.T) {
	popupProgram(t, func(m formModel) formModel { m, _ = popupPress(m, "n"); return m })
	idx, key, ok := testPickerForm().run()
	if idx != -1 || key != 'n' || !ok {
		t.Fatalf("got (%d,%q,%v)", idx, key, ok)
	}
}

// TestPickerTakesTypedNumbersPastNine preserves pending digits and invalid-number retries.
func TestPickerTakesTypedNumbersPastNine(t *testing.T) {
	initial := formModel{kind: formPick, items: make([]string, 12)}
	m, c := popupPress(initial, "1")
	if m.ok || c != nil {
		t.Fatal("bare digit selected on long list")
	}
	m, c = popupPress(m, "2", "enter")
	if !m.ok || m.index != 11 || c == nil {
		t.Fatal("typed 12 did not select")
	}
	m, c = popupPress(initial, "9", "9", "enter")
	if m.ok || c != nil || m.typed != "" {
		t.Fatal("invalid number did not clear")
	}
	m, c = popupPress(m, "7", "enter")
	if !m.ok || m.index != 6 || c == nil {
		t.Fatal("retry did not select")
	}
}

// TestConfirmInFrame pins yes, no, default and cancellation on the replacement form.
func TestConfirmInFrame(t *testing.T) {
	for _, tc := range []struct {
		key     string
		val, ok bool
	}{{"y", true, true}, {"n", false, true}, {"enter", false, true}, {"esc", false, false}, {"q", false, false}, {"ctrl+c", false, false}, {"ctrl+\\", false, false}} {
		t.Run(tc.key, func(t *testing.T) {
			m, c := popupPress(formModel{kind: formConfirm}, tc.key)
			if m.value != tc.val || m.ok != tc.ok || c == nil {
				t.Fatalf("got (%v,%v)", m.value, m.ok)
			}
		})
	}
}

// TestFormReadersWithoutATTY checks that forms never start a program on a pipe.
func TestFormReadersWithoutATTY(t *testing.T) {
	popupStdin(t, "")
	old := formProgram
	formProgram = func(m tea.Model) (tea.Model, error) { t.Fatal("program started on pipe"); return m, nil }
	t.Cleanup(func() { formProgram = old })
	for _, m := range []tea.Model{formModel{kind: formChoice}, formModel{kind: formPick}, formModel{kind: formAsk}, formModel{kind: formConfirm}, formModel{kind: formNote}} {
		if formState(runForm(m)).ok {
			t.Fatal("headless form returned an answer")
		}
	}
	if n, k, ok := (formModel{kind: formPick}).run(); n != -1 || k != 0 || ok {
		t.Fatal("empty picker selected")
	}
}
