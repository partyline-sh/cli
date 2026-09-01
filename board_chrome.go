package main

import (
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
)

// board_chrome.go — the board's measuring, clipping and hint bar.
//
// Width handling goes through internal/brand rather than len(): every one of these strings carries
// ANSI colour, and a byte count would make the borders disagree with what is on screen by however
// many escape bytes a line happened to hold.

func visWidth(s string) int { return brand.VisWidth(s) }
func clipVis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return brand.ClipEllipsis(s, w)
}

// padVis pads to w columns, clipping first when the content is already too wide, so a tile can
// never push its neighbour sideways.
func padVis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if visWidth(s) > w {
		s = brand.ClipEllipsis(s, w)
	}
	return s + strings.Repeat(" ", max(0, w-visWidth(s)))
}

func boardWordmark() string { return brand.Wordmark() }

// hintLine is the keys, filtered to what is actually available on the focused card. A hint bar that
// lists moves the current card does not have teaches the wrong thing on every board that is not the
// one it was written against.
func (m *boardModel) hintLine(w int) string {
	hints := []brand.Hint{{Key: "↑↓←→", Label: "move"}}

	if r, ok := m.focusedRow(); ok && r.header() {
		hints = append(hints, brand.Hint{Key: "⏎", Label: "fold/unfold"})
	} else if c, ok := m.focused(); ok {
		if a, has := primaryAction(*c); has {
			hints = append(hints, brand.Hint{Key: "⏎", Label: strings.ToLower(a.Label)})
		}
		hints = append(hints, brand.Hint{Key: "a", Label: "actions"}, brand.Hint{Key: "d", Label: "detail"})
		// Which column the CURSOR is in, not the card's own `column` field. They agree on a fresh
		// board, but the cursor's position is what the reader can see, and a card whose field is
		// stale would otherwise offer the wrong keys with no way to tell why.
		switch m.focusedColumn() {
		case api.ColBuilding, api.ColBlocked:
			hints = append(hints, brand.Hint{Key: "s", Label: "session"})
		case api.ColReview:
			hints = append(hints, brand.Hint{Key: "r", Label: "review diff"})
		}
		if c.PRURL != "" {
			hints = append(hints, brand.Hint{Key: "o", Label: "open PR"})
		}
	}
	hints = append(hints,
		brand.Hint{Key: "n", Label: "new"},
		brand.Hint{Key: "g", Label: "refresh"},
		brand.Hint{Key: "q", Label: "quit"},
	)
	return brand.HintBar("board", hints, w)
}
