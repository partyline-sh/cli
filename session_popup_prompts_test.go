package main

import (
	"strings"
	"testing"
)

// testSessionPopupPrompts exercises public prompts with forms and piped lines.
func testSessionPopupPrompts(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want string
		ok   bool
	}{
		{[]string{"enter"}, "default", true}, {[]string{"typed", "enter"}, "typed", true}, {[]string{"q", "enter"}, "", false},
		{[]string{"esc"}, "", false}, {[]string{"ctrl+c"}, "", false}, {[]string{"ctrl+\\"}, "", false},
		{[]string{"ab", "backspace", "enter"}, "a", true}, {[]string{"ab", "ctrl+u", "enter"}, "default", true},
	} {
		t.Run("tty-input-"+strings.Join(tc.keys, "-"), func(t *testing.T) {
			popupProgram(t, func(m formModel) formModel {
				if m.kind != formAsk {
					t.Fatal("Input did not use formAsk")
				}
				m, _ = popupPress(m, tc.keys...)
				return m
			})
			got, ok := Input("name", "default")
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q,%v)", got, ok)
			}
		})
	}
	for _, k := range []string{"y", "Y", "n", "N", "enter", "q", "Q", "esc", "ctrl+c", "ctrl+\\"} {
		t.Run("tty-confirm-"+k, func(t *testing.T) {
			popupProgram(t, func(m formModel) formModel {
				if m.kind != formConfirm {
					t.Fatal("Confirm did not use formConfirm")
				}
				m, _ = popupPress(m, k)
				return m
			})
			value, ok := Confirm("continue?", true)
			wantOK := strings.EqualFold(k, "y") || strings.EqualFold(k, "n") || k == "enter"
			want := strings.EqualFold(k, "y") || k == "enter"
			if value != want || ok != wantOK {
				t.Fatalf("got (%v,%v)", value, ok)
			}
		})
	}
	for _, tc := range []struct {
		keys         []string
		count, index int
		ok           bool
	}{
		{[]string{"1"}, 3, 0, true}, {[]string{"2"}, 3, 1, true}, {[]string{"3"}, 3, 2, true}, {[]string{"down", "enter"}, 3, -1, false}, {[]string{"up", "2"}, 3, 1, true},
		{[]string{"1", "2", "enter"}, 12, 11, true}, {[]string{"enter"}, 3, -1, false}, {[]string{"1", "3", "enter"}, 12, -1, false}, {[]string{"q"}, 3, -1, false}, {[]string{"esc"}, 3, -1, false}, {[]string{"ctrl+c"}, 3, -1, false}, {[]string{"ctrl+\\"}, 3, -1, false},
	} {
		t.Run("tty-pick-"+strings.Join(tc.keys, "-"), func(t *testing.T) {
			popupProgram(t, func(m formModel) formModel {
				if m.kind != formPick {
					t.Fatal("Pick did not use formPick")
				}
				m, _ = popupPress(m, tc.keys...)
				return m
			})
			got, ok := Pick("pick", make([]string, tc.count), func(s string) string { return s })
			if got != tc.index || ok != tc.ok {
				t.Fatalf("got (%d,%v)", got, ok)
			}
		})
	}
	// With no terminal on stdin every prompt cancels at once and reads nothing: a pipe or the
	// daemon must never hang on a question, and a piped line must not be taken as an answer.
	t.Run("no-tty-cancels", func(t *testing.T) {
		popupStdin(t, "y\nname\n2\n")
		if s, ok := Input("name", "default"); ok || s != "" {
			t.Errorf("Input with no tty: (%q,%v)", s, ok)
		}
		if v, ok := Confirm("continue?", true); ok || v {
			t.Errorf("Confirm with no tty: (%v,%v)", v, ok)
		}
		if n, ok := Pick("pick", []string{"a", "b"}, func(s string) string { return s }); ok || n != -1 {
			t.Errorf("Pick with no tty: (%d,%v)", n, ok)
		}
		if line, _ := stdin().ReadString('\n'); line != "y\n" {
			t.Errorf("a prompt read stdin: next line %q", line)
		}
	})
}
