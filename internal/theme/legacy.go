package theme

import "fmt"

// Legacy roles preserve the existing terminal colours in Partyline dark.
// QRContrast keeps machine-readable QR modules black on white in every theme.
const QRContrast = "\x1b[30;47m"

const (
	LegacyYou         = 117
	LegacyCodex       = 80
	LegacyGemini      = 75
	LegacyLLM         = 207
	LegacyAntigravity = 42
	LegacyMuted       = 245
	LegacyWire        = 39
	LegacyAccent      = 215
	LegacyText        = 231
	LegacyBranch      = 114
	LegacyClean       = 108
	LegacyDanger      = 203
	LegacyFaint       = 240
	LegacyLabel       = 243
	LegacyWaiting     = 214
	LegacyRunning     = 46
	LegacyHome        = 139
	LegacyRule        = 237
	LegacyBody        = 250
)

const (
	DefaultBorder     = "\x1b[38;5;215m"
	CLIMuted          = "\x1b[38;5;245m"
	CLIKey            = "\x1b[36m"
	CLIWire           = "\x1b[38;5;39m"
	CLIOK             = "\x1b[32m"
	CLIBad            = "\x1b[31m"
	HintKey           = "\x1b[1;38;5;215m"
	HintLabel         = "\x1b[38;5;245m"
	HintSeparator     = "\x1b[38;5;240m"
	PillBackground256 = "\x1b[48;5;206m"
	PillBackgroundRGB = "\x1b[48;2;255;94;138m"
	PillForeground    = "\x1b[1;38;5;16m"
)

// IndexedForeground builds the ANSI fallback used by the brand wordmark.
func IndexedForeground(index int) string { return fmt.Sprintf("\x1b[38;5;%dm", index) }

func RGBForeground(c [3]int) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
}

// LegacyForeground keeps Partyline dark byte-for-byte; other palettes use their roles.
func (t Theme) LegacyForeground(role int) string {
	if t.Palette == "Partyline" && t.Dark {
		return IndexedForeground(role)
	}
	colour := t.Text
	switch role {
	case LegacyMuted, LegacyLabel:
		colour = t.Muted
	case LegacyFaint, LegacyRule:
		colour = t.Faint
	case LegacyAccent:
		colour = t.Claude
	case LegacyWaiting:
		colour = t.Warning
	case LegacyRunning, LegacyBranch, LegacyClean:
		colour = t.Success
	case LegacyYou:
		colour = t.You
	case LegacyAntigravity:
		colour = t.Success
	case LegacyDanger:
		colour = t.Danger
	case LegacyHome:
		colour = t.LLM
	case LegacyCodex:
		colour = t.Codex
	case LegacyGemini:
		colour = t.Gemini
	case LegacyLLM:
		colour = t.LLM
	}
	var c [3]int
	fmt.Sscanf(colour, "#%02x%02x%02x", &c[0], &c[1], &c[2])
	return RGBForeground(c)
}

// Chat foregrounds retain the basic ANSI defaults used by Markdown and the inline editor.
func (t Theme) ChatCode() string {
	if t.Palette == "Partyline" && t.Dark {
		return "\x1b[36m"
	}
	return t.LegacyForeground(LegacyCodex)
}
func (t Theme) ChatHeading() string {
	if t.Palette == "Partyline" && t.Dark {
		return "\x1b[1;35m"
	}
	return "\x1b[1m" + t.LegacyForeground(LegacyHome)
}

// ChatName is a speaker's name in the chat room: yours apart from everyone else's.
func (t Theme) ChatName(you bool) string {
	if t.Palette == "Partyline" && t.Dark {
		if you {
			return "\x1b[1;33m"
		}
		return "\x1b[1;36m"
	}
	if you {
		return t.ChatPrompt()
	}
	return "\x1b[1m" + t.LegacyForeground(LegacyCodex)
}

func (t Theme) ChatPrompt() string {
	if t.Palette == "Partyline" && t.Dark {
		return "\x1b[1;33m"
	}
	var c [3]int
	fmt.Sscanf(t.Focus, "#%02x%02x%02x", &c[0], &c[1], &c[2])
	return "\x1b[1m" + RGBForeground(c)
}
