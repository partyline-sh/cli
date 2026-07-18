package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A theme re-skins the llms launcher by remapping the dark-default ANSI-256 colour indexes
// the renderer emits to a palette tuned for a given background. We remap the FINAL rendered
// frame (see themed) rather than editing every call site, so one table re-skins the whole
// UI and Midnight (no map) leaves dark terminals byte-for-byte unchanged.
//
// The renderer's source palette (what the indexes below mean): 252 text · 250 project label
// · 245/243/242 dim · 240 faint hint · 238/237 frame/divider · 231 selected text · 236 (bg)
// selection band · 215 accent/claude · 214 waiting · 46 running · 114/108 ok/clean · 51
// marked · 203 error · 117 "you" · 80/75/207 codex/gemini/llm · 208–220 banner gradient.
//
// Light themes follow one rule: text/labels become DARK indexes (legible on white), pastels
// are only frames/selection/accents. Codes are best-effort and meant to be eyeballed on a
// real terminal and nudged — the structure is what matters.
type palette struct {
	name  string
	forBg string
	remap map[string]string // source ANSI-256 index → themed index; nil = identity (Midnight)
}

var themes = []palette{
	{name: "Midnight", forBg: "dark terminals"}, // identity — the original look, unchanged

	{name: "Daylight", forBg: "light background", remap: map[string]string{
		"231": "16", "252": "236", "250": "238", "245": "240", "243": "239", "242": "243",
		"240": "244", "238": "250", "237": "251", "236": "153",
		"215": "31", "214": "130", "46": "28", "114": "28", "108": "29", "51": "31",
		"203": "160", "117": "25", "111": "31", "80": "30", "75": "25", "207": "90",
	}},

	{name: "Raging Unicorn 🦄", forBg: "light background", remap: map[string]string{
		"231": "53", "252": "54", "250": "96", "245": "97", "243": "96", "242": "132",
		"240": "175", "238": "218", "237": "219", "236": "219",
		"215": "205", "214": "172", "46": "36", "114": "36", "108": "37", "51": "39",
		"203": "197", "117": "39", "111": "205", "80": "37", "75": "39", "207": "164",
		// banner gradient → pink/purple
		"208": "205", "209": "206", "220": "212", "213": "177", "212": "141", "211": "135", "205": "205", "204": "206",
	}},

	{name: "Cotton Sky", forBg: "light background", remap: map[string]string{
		"231": "23", "252": "24", "250": "60", "245": "66", "243": "60", "242": "66",
		"240": "110", "238": "117", "237": "152", "236": "195",
		"215": "39", "214": "172", "46": "30", "114": "30", "108": "36", "51": "169",
		"203": "161", "117": "33", "111": "39", "80": "37", "75": "33", "207": "169",
		// banner gradient → blues
		"208": "39", "209": "38", "220": "45", "213": "75", "212": "111", "211": "75", "205": "39", "204": "38",
	}},

	{name: "Paperwhite", forBg: "light · high-contrast", remap: map[string]string{
		"231": "16", "252": "16", "250": "236", "245": "238", "243": "238", "242": "240",
		"240": "240", "238": "245", "237": "248", "236": "252",
		"215": "20", "214": "94", "46": "22", "114": "22", "108": "28", "51": "18",
		"203": "124", "117": "19", "111": "20", "80": "23", "75": "19", "207": "53",
		// banner gradient → solid near-black (max contrast)
		"208": "16", "209": "16", "220": "16", "213": "16", "212": "16", "211": "16", "205": "16", "204": "16",
	}},
}

var (
	themeIdx int
	theme    = themes[0]
)

var sgrRe = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// themed rewrites the ANSI-256 colour indexes in a fully-rendered frame to the active theme.
// Only fg (38;5;N) and bg (48;5;N) indexes are translated; bold/italic/reset pass through.
func themed(s string) string {
	if len(theme.remap) == 0 {
		return s // Midnight — identity
	}
	return sgrRe.ReplaceAllStringFunc(s, func(seq string) string {
		toks := strings.Split(seq[2:len(seq)-1], ";")
		for i := 0; i+2 < len(toks); i++ {
			if (toks[i] == "38" || toks[i] == "48") && toks[i+1] == "5" {
				if r, ok := theme.remap[toks[i+2]]; ok {
					toks[i+2] = r
				}
			}
		}
		return "\x1b[" + strings.Join(toks, ";") + "m"
	})
}

func themePath() string { return filepath.Join(stateDir(), "llms-theme") }

// loadTheme restores the saved theme by name (default Midnight). Call once at launch.
func loadTheme() {
	b, err := os.ReadFile(themePath())
	if err != nil {
		return
	}
	name := strings.TrimSpace(string(b))
	for i, t := range themes {
		if t.name == name {
			themeIdx, theme = i, themes[i]
			return
		}
	}
}

// cycleTheme advances to the next theme, persists it, and returns it (for a footer flash).
func cycleTheme() palette {
	themeIdx = (themeIdx + 1) % len(themes)
	theme = themes[themeIdx]
	_ = os.MkdirAll(filepath.Dir(themePath()), 0o700)
	_ = os.WriteFile(themePath(), []byte(theme.name+"\n"), 0o600)
	return theme
}
