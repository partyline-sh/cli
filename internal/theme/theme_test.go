package theme

import (
	"math"
	"strings"
	"testing"
)

// Every token is set in both variants, as a colour the tmux conf and Lip Gloss both accept.
func TestEveryTokenIsAColourInBothVariants(t *testing.T) {
	for _, th := range All() {
		if err := th.Tokens.Check(); err != nil {
			t.Errorf("%s dark=%v: %v", th.Palette, th.Dark, err)
		}
	}
	if Default().Dark != true || Light().Dark != false {
		t.Error("variants are labelled the wrong way round")
	}
}

// Each state has a glyph and a name.
func TestStateVocabularyIsComplete(t *testing.T) {
	for _, s := range States {
		if s.Glyph() == "?" || s.Name() == "" {
			t.Errorf("state %d has no glyph or name", s)
		}
	}
	if len([]rune(NeedsYouFallback)) != 1 {
		t.Error("the fallback must be a single character")
	}
}

// State colours carry meaning: they differ from the accent in both variants.
func TestStateColoursAreNotTheAccent(t *testing.T) {
	for _, th := range All() {
		for _, c := range []string{th.Warning, th.Danger, th.Success} {
			if strings.EqualFold(c, th.Focus) {
				t.Errorf("a state colour equals the focus colour %s", c)
			}
		}
	}
}

func TestBlendRunsFromTheFirstStopToTheLast(t *testing.T) {
	g := Default().Blend(9)
	if len(g) != 9 || g[0] != "#ffaa3c" || g[4] != "#ff5e8a" || g[8] != "#df5bff" {
		t.Errorf("blend = %v", g)
	}
	if len(Default().Blend(1)) != 1 || Default().Blend(0) != nil {
		t.Error("edge sizes")
	}
}

// Every token is checked by walking the struct: an empty palette fails, naming a token.
func TestCheckCoversEveryToken(t *testing.T) {
	if err := (Tokens{}).Check(); err == nil {
		t.Error("an empty palette passed Check")
	}
	broken := Default().Tokens
	broken.Border = "orange"
	if err := broken.Check(); err == nil || !strings.Contains(err.Error(), "Border") {
		t.Errorf("a bad Border was not reported: %v", err)
	}
}

func luminance(hex string) float64 {
	c := rgb(hex)
	var l [3]float64
	for i, v := range c {
		x := float64(v) / 255
		if x <= 0.03928 {
			l[i] = x / 12.92
		} else {
			l[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Text and markers stay readable on both grounds in both variants (WCAG ratios: 7 for body text,
// 4 for secondary text, 3 for faint text, state markers and frames).
func TestReadableContrast(t *testing.T) {
	for _, th := range All() {
		for _, bg := range []string{th.Ground, th.Surface} {
			for name, c := range map[string]struct {
				fg  string
				min float64
			}{
				"Text": {th.Text, 7}, "Muted": {th.Muted, 4}, "Faint": {th.Faint, 3}, "Border": {th.Border, 3},
				"Focus": {th.Focus, 3}, "Success": {th.Success, 3}, "Warning": {th.Warning, 3},
				"Attention": {th.Attention, 3}, "Danger": {th.Danger, 3},
			} {
				if r := contrast(c.fg, bg); r < c.min {
					t.Errorf("dark=%v %s %s on %s: %.2f:1, want at least %.0f:1", th.Dark, name, c.fg, bg, r, c.min)
				}
			}
		}
	}
}

// Glyphs are unique, and state colours are distinct from each other (Working shares Focus by
// design, so it is left out of the colour comparison).
func TestStatesAreDistinctByGlyphAndColour(t *testing.T) {
	glyphs := map[string]bool{}
	for _, s := range States {
		if glyphs[s.Glyph()] {
			t.Errorf("glyph %s used twice", s.Glyph())
		}
		glyphs[s.Glyph()] = true
	}
	for _, th := range All() {
		colours := map[string]State{}
		for _, s := range []State{NeedsYou, Idle, Passed, Failed} {
			if prev, ok := colours[th.Colour(s)]; ok {
				t.Errorf("dark=%v: %s and %s share colour %s", th.Dark, prev.Name(), s.Name(), th.Colour(s))
			}
			colours[th.Colour(s)] = s
		}
	}
}
