package main

import (
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// formRow is a single-key action with an optional explanation.
type formRow struct{ key, label, note string }

// formExtra is a letter action offered alongside numbered items.
type formExtra struct {
	Key   rune
	Label string
}

// formKind is which form a formModel is; each has its own keys (Update) and layout (View).
type formKind int

const (
	formChoice  formKind = iota // one-key actions
	formPick                    // numbered items and letter actions
	formAsk                     // a line of text with a default
	formConfirm                 // yes or no
	formNote                    // a message any key dismisses
)

// formModel is one popup form, in display-popup: what it shows, what has been typed, and its
// result, read once its program exits. ok=false is a cancel, distinct from a deliberate no.
type formModel struct {
	kind                               formKind
	title, label, def, typed, answer   string
	body, items                        []string
	rows                               []formRow
	extras                             []formExtra
	enterCancel, defaultYes, value, ok bool
	cancelUnknown                      bool // Menu semantics: unknown keys close; escape sequences are ignored
	off, index, w, h                   int
	key                                rune
}

// formState is the form a finished program returns.
func formState(m tea.Model) formModel { return m.(formModel) }

// formTerminal reports whether forms can own stdin.
var formTerminal = stdinIsTTY

// formProgram runs a terminal program; tests replace it with direct updates.
var formProgram = func(m tea.Model) (tea.Model, error) { return tea.NewProgram(m).Run() }

// runForm restores the terminal before returning the completed model.
func runForm(m tea.Model) tea.Model {
	if !formTerminal() {
		return m
	}
	final, err := formProgram(m)
	if err != nil {
		return m
	}
	return final
}

// Init starts a form without side effects.
func (m formModel) Init() tea.Cmd { return nil }

// Update handles popup keys without performing the selected action.
func (m formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.PasteMsg:
		if m.kind == formAsk {
			m.typed += strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, msg.Content)
		}
	case tea.KeyPressMsg:
		k := strings.ToLower(msg.String())
		if m.kind == formNote {
			if m.cancelUnknown && (msg.Code > unicode.MaxRune || msg.Mod&tea.ModAlt != 0) {
				return m, nil
			}
			return m, tea.Quit
		}
		if k == "esc" || k == "ctrl+c" || k == "ctrl+\\" {
			return m, tea.Quit
		}
		if m.kind != formAsk && k == "q" {
			return m, tea.Quit
		}
		switch m.kind {
		case formChoice:
			if m.cancelUnknown && msg.Mod&tea.ModAlt != 0 {
				return m, nil
			}
			if k == "enter" && m.enterCancel {
				return m, tea.Quit
			}
			for _, row := range m.rows {
				if strings.ToLower(msg.Text) == row.key {
					m.key, m.ok = []rune(row.key)[0], true
					return m, tea.Quit
				}
			}
			if m.cancelUnknown && (msg.Text != "" || msg.Code == tea.KeyTab || msg.Code == tea.KeyBackspace || k == "enter" || msg.Mod&tea.ModCtrl != 0 && msg.Code <= unicode.MaxRune) {
				// ctrl with a letter closes, as the old key reader did; ctrl with an arrow or function key is an
				// escape sequence the old key reader ignored
				return m, tea.Quit
			}

		case formConfirm:
			switch k {
			case "y", "n", "enter":
				m.value, m.ok = k == "y" || k == "enter" && m.defaultYes, true
				return m, tea.Quit
			}
		case formAsk:
			switch k {
			case "enter":
				s := strings.TrimSpace(m.typed)
				if strings.EqualFold(s, "q") {
					return m, tea.Quit
				}
				if s == "" {
					s = m.def
				}
				m.answer, m.ok = s, s != ""
				return m, tea.Quit
			case "backspace", "ctrl+h":
				r := []rune(m.typed)
				if len(r) > 0 {
					m.typed = string(r[:len(r)-1])
				}
			case "ctrl+u":
				m.typed = ""
			default:
				if msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 { // ctrl/alt chords are not text
					m.typed += msg.Text
				}
			}
		case formPick:
			switch k {
			case "up": // ↑↓ scroll a list taller than the popup; picking is by number
				m.off = max(m.off-1, 0)
			case "down":
				m.off = min(m.off+1, max(len(m.items)-m.pickRows(), 0))
			case "enter":
				// Enter closes, as on every other ctrl-\ screen, unless it confirms a typed number
				// (a long list's "12 ⏎"). Out of range selects nothing and lets you type again.
				if m.typed == "" {
					return m, tea.Quit
				}
				v, err := strconv.Atoi(m.typed)
				m.typed = ""
				if err != nil || v < 1 || v > len(m.items) {
					return m, nil
				}
				m.index, m.ok = v-1, true
				return m, tea.Quit
			case "backspace":
				if len(m.typed) > 0 {
					m.typed = m.typed[:len(m.typed)-1]
				}
			default:
				if len(k) == 1 && k[0] >= '0' && k[0] <= '9' {
					if len(m.items) > 9 {
						if len(m.typed) < len(strconv.Itoa(len(m.items))) {
							m.typed += k
						}
					} else if n := int(k[0] - '1'); n >= 0 && n < len(m.items) {
						m.index, m.ok = n, true
						return m, tea.Quit
					}
				} else {
					for _, e := range m.extras {
						if k == strings.ToLower(string(e.Key)) {
							m.key, m.ok = e.Key, true
							return m, tea.Quit
						}
					}
				}
			}
		}
	}
	return m, nil
}
