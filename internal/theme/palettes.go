package theme

import "strings"

// The palettes a person picks (docs/plans/bubbletea-ui.md §3, "Themes the person picks"). Neutrals
// and accents are the website's: web/src/app/globals.css html[data-palette="blue"|"mono"], with and
// without .dark. State, engine, marked and you colours are the Partyline ones for the background,
// because they carry meaning and must not change with taste. Faint and Border are darkened or
// lightened from the website's where those measured under 3:1 (TestReadableContrast).

func withStates(t Tokens, dark bool) Tokens {
	src := partylineLight
	if dark {
		src = partylineDark
	}
	t.Success, t.Warning, t.Attention, t.Danger = src.Success, src.Warning, src.Attention, src.Danger
	t.Claude, t.Codex, t.Gemini, t.LLM = src.Claude, src.Codex, src.Gemini, src.LLM
	t.Marked, t.You = src.Marked, src.You
	return t
}

var (
	blueDark = withStates(Tokens{
		Ground: "#13171e", Surface: "#1b212a", Raised: "#222a35", Rule: "#333d4b", Border: "#6b7a8f",
		Text: "#eef2f8", Muted: "#9dabbd", Faint: "#7d8a9c",
		Gradient: [3]string{"#a7cbf9", "#5c9bf5", "#3b6fd8"},
		Focus:    "#5c9bf5",
	}, true)
	blueLight = withStates(Tokens{
		Ground: "#f1f5f9", Surface: "#ffffff", Raised: "#e2e8f0", Rule: "#e2e8f0", Border: "#7b8798",
		Text: "#0f172a", Muted: "#5b6b7f", Faint: "#7b8798",
		Gradient: [3]string{"#7db2ff", "#1a3fa8", "#0f2a75"},
		Focus:    "#1a3fa8",
	}, false)
	monoDark = withStates(Tokens{
		Ground: "#161618", Surface: "#1f1f22", Raised: "#2a2a2e", Rule: "#3a3a3f", Border: "#77777f",
		Text: "#f4f4f5", Muted: "#a1a1aa", Faint: "#85858d",
		Gradient: [3]string{"#d4d4d8", "#a1a1aa", "#71717a"},
		Focus:    "#c4c4cc",
	}, true)
	monoLight = withStates(Tokens{
		Ground: "#f4f4f5", Surface: "#ffffff", Raised: "#e4e4e7", Rule: "#e4e4e7", Border: "#85858d",
		Text: "#18181b", Muted: "#6b6b73", Faint: "#85858d",
		Gradient: [3]string{"#3a3a41", "#6b6b73", "#a1a1aa"},
		Focus:    "#3a3a41",
	}, false)
	// Paperwhite is today's high-contrast light look (llms_theme.go), its ANSI-256 indices as hex:
	// text 16, labels 236, hints 240, frames 252, accent 20, the banner solid near-black.
	paperwhite = withStates(Tokens{
		Ground: "#ffffff", Surface: "#eeeeee", Raised: "#d0d0d0", Rule: "#d0d0d0", Border: "#585858",
		Text: "#000000", Muted: "#303030", Faint: "#585858",
		Gradient: [3]string{"#000000", "#000000", "#000000"},
		Focus:    "#0000d7",
	}, false)
)

// Palettes is every palette, in the order the launcher's t cycles them.
var Palettes = []string{"Partyline", "Blue", "Mono", "Paperwhite"}

// Lookup is the named palette (any case) for a dark or light terminal. Paperwhite has only a light
// variant, which is what it gives for either.
func Lookup(name string, dark bool) (Theme, bool) {
	var t Tokens
	switch strings.ToLower(name) {
	case "partyline":
		t, name = pick(dark, partylineDark, partylineLight), "Partyline"
	case "blue":
		t, name = pick(dark, blueDark, blueLight), "Blue"
	case "mono":
		t, name = pick(dark, monoDark, monoLight), "Mono"
	case "paperwhite":
		t, name, dark = paperwhite, "Paperwhite", false
	default:
		return Theme{}, false
	}
	return Theme{Palette: name, Dark: dark, Tokens: t}, true
}

func pick(dark bool, d, l Tokens) Tokens {
	if dark {
		return d
	}
	return l
}

// All is every palette and variant, for tests.
func All() []Theme {
	var out []Theme
	for _, p := range Palettes {
		for _, dark := range []bool{true, false} {
			if t, _ := Lookup(p, dark); t.Dark == dark {
				out = append(out, t)
			}
		}
	}
	return out
}
