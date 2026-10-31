// Package theme is the one place partyline defines a colour, a glyph or a state marker. The tmux
// conf, and every Bubble Tea screen as it is built, read from here, so the tab bar, popups and
// screens look like one product (docs/plans/bubbletea-ui.md §3).
//
// The values come from the website's own tokens (web/src/app/globals.css: the .dark and light
// themes and the marketing brand layer). State colours carry meaning and are the same in every
// palette; a palette changes the neutrals and the accent only.
package theme

import (
	"fmt"
	"reflect"
	"regexp"
)

// Tokens is one palette variant: every colour a surface may use, as #rrggbb.
type Tokens struct {
	Ground, Surface, Raised     string // backgrounds
	Rule                        string // decorative separators only: deliberately quiet
	Border                      string // frames that separate one surface from another: at least 3:1
	Text, Muted, Faint          string // foreground hierarchy
	Gradient                    [3]string
	Focus                       string // cursor, active input, primary action, the current tab
	Success, Warning, Attention string
	Danger                      string
	Claude, Codex, Gemini, LLM  string // engine identity
	Marked, You                 string // multi-select mark; your own messages
}

// Theme is a named palette with the variant for the terminal's background.
type Theme struct {
	Palette string
	Dark    bool
	Tokens
}

// partylineDark and partylineLight are the default palette. Engine, marked and you colours keep
// today's identity: dark is the launcher's ANSI-256 indices (215, 80, 75, 207, 51, 117) as hex;
// light is the same indices through today's Daylight remap (31, 30, 25, 90, 31, 25), as hex.
// Faint and Border are not the website's values: theirs measured under 3:1 on these grounds
// (TestReadableContrast).
var (
	partylineDark = Tokens{
		Ground: "#1b1a19", Surface: "#232220", Raised: "#2a2826", Rule: "#38352f", Border: "#78716c",
		Text: "#f5f3f0", Muted: "#a8a29e", Faint: "#8a8580",
		Gradient: [3]string{"#ffaa3c", "#ff5e8a", "#df5bff"},
		Focus:    "#ff5e8a",
		Success:  "#adf7b6", Warning: "#ffc09f", Attention: "#ffee93", Danger: "#ef4444",
		Claude: "#ffaf5f", Codex: "#5fd7d7", Gemini: "#5fafff", LLM: "#ff5fff",
		Marked: "#00ffff", You: "#87d7ff",
	}
	partylineLight = Tokens{
		Ground: "#ffffff", Surface: "#f5f5f4", Raised: "#e7e5e4", Rule: "#e7e5e4", Border: "#8a8580",
		Text: "#1c1917", Muted: "#78716c", Faint: "#8a8580",
		Gradient: [3]string{"#ffaa3c", "#ff5e8a", "#df5bff"},
		Focus:    "#9e1a58",
		// State colours keep their meaning; on a light ground they are the website's light-theme
		// foreground shades (--success-foreground etc.), so they stay readable.
		Success: "#14532d", Warning: "#7c2d12", Attention: "#713f12", Danger: "#dc2626",
		Claude: "#0087af", Codex: "#008787", Gemini: "#005faf", LLM: "#870087",
		Marked: "#0087af", You: "#005faf",
	}
)

// Default is Partyline dark: the fallback when nothing is chosen, and what tests compare against.
func Default() Theme { return Theme{Palette: "Partyline", Dark: true, Tokens: partylineDark} }

// Light is the Partyline palette for light terminals.
func Light() Theme { return Theme{Palette: "Partyline", Dark: false, Tokens: partylineLight} }

// State is one of the states every surface shows the same way.
type State int

const (
	Working State = iota
	NeedsYou
	Idle
	Passed
	Failed
)

// States lists every state, for tests and for the contact sheet.
var States = []State{Working, NeedsYou, Idle, Passed, Failed}

// Glyph is the state's marker. Working is a spinner in screens; Glyph gives its still form,
// which is what the tmux tab bar shows (tmux redraws on an interval, it cannot animate).
//
// NeedsYou is U+203C. tmux 3.7c gives it one cell (measured: cursor at column 1 after printing
// it); NeedsYouFallback is used if a terminal is found to draw it two cells wide, which corrupts
// rows under tmux (ultraviolet #164).
func (s State) Glyph() string {
	switch s {
	case Working:
		return "/"
	case NeedsYou:
		return "‼"
	case Idle:
		return "·"
	case Passed:
		return "✓"
	case Failed:
		return "✗"
	}
	return "?"
}

// NeedsYouFallback replaces the needs-you glyph where it would be drawn two cells wide.
const NeedsYouFallback = "▲"

// Colour is the state's colour in t.
func (t Theme) Colour(s State) string {
	switch s {
	case Working:
		return t.Focus
	case NeedsYou:
		return t.Warning
	case Idle:
		return t.Faint
	case Passed:
		return t.Success
	case Failed:
		return t.Danger
	}
	return t.Text
}

// Name is the state as people read it.
func (s State) Name() string {
	return [...]string{"working", "needs you", "idle", "passed", "failed"}[s]
}

var hexColour = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Check reports the first token, in field order, that is missing or not a #rrggbb colour. It walks
// the struct, so a token added to Tokens is checked without being listed anywhere.
func (t Tokens) Check() error {
	v := reflect.ValueOf(t)
	for i := 0; i < v.NumField(); i++ {
		f, name := v.Field(i), v.Type().Field(i).Name
		vals := []string{}
		switch f.Kind() {
		case reflect.String:
			vals = append(vals, f.String())
		case reflect.Array:
			for j := 0; j < f.Len(); j++ {
				vals = append(vals, f.Index(j).String())
			}
		}
		for _, c := range vals {
			if !hexColour.MatchString(c) {
				return fmt.Errorf("token %s = %q is not a #rrggbb colour", name, c)
			}
		}
	}
	return nil
}

// Glyphs the tmux bar and screens draw that are not states.
const (
	Phone    = "☎" // the wordmark's mark (U+260E; one cell in tmux 3.7c, measured)
	Shared   = "⇡" // a window shared with others
	Ellipsis = "…" // a truncated name
	Gear     = "⚙" // a contractor: an agent partyline started for a loop
)
