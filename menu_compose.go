package main

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/brand"
)

// THE MULTI-LINE COMPOSE FIELD — for the one input that is genuinely large.
//
// cgAsk (menu_ask.go) is a LINE: enter submits, and it is right for a worktree name or a thread title.
// A peer question is not a line. The questions people actually send are excerpts pasted out of an LLM
// discussion — hundreds of words, with newlines. In cgAsk that paste submitted at the first newline
// and dropped the rest, silently. So this is a separate reader with a separate contract, and cgAsk is
// untouched: the single-line screens keep the single-line behaviour they want.
//
// THE CONTRACT (and it is in the hint bar, because a key you can't guess doesn't exist):
//
//	⏎        insert a newline. NOT send. Everything else follows from this one decision.
//	ctrl-d   send. The conventional "I'm done" for a multi-line TUI field.
//	ctrl-e   hand off to $EDITOR and read the result back — for text you want to actually edit.
//	esc      abandon (so do ctrl-c and ctrl-\), exactly as every other ctrl-\ screen.
//	ctrl-u   clear · backspace deletes one character, including a newline.
//	a paste  arrives as TEXT, verbatim, newlines included (menu_paste.go).
//
// LAYOUT. The text is a scrolling region of INTERIOR frame lines, and the last visible line is the
// modal's Prompt row so the cursor lands where you are typing (menu_modal.go puts it at the end of
// that row). The region is windowed to the rows the terminal actually has — a long question scrolls
// inside the box and the box never grows past the screen, because a frame taller than the terminal
// scrolls the terminal and tears the border apart. That bug is fixed; this does not reintroduce it.

// cgComposeOutcome is why a compose session ended. The $EDITOR hand-off has to be a distinct outcome
// rather than something done in place: the child process needs the tty in its ORIGINAL cooked mode, so
// the only correct place to run it is outside cgRaw — which means unwinding the reader and coming back.
type cgComposeOutcome int

const (
	cgComposeAbandon cgComposeOutcome = iota
	cgComposeSend
	cgComposeEditor
)

// cgComposeGutter marks the first line of the region with the established `› ` idiom and indents the
// continuations, so a wrapped question reads as one field rather than as loose body text.
func cgComposeGutter(i int) string {
	if i == 0 {
		return sgr(cgKey, "›") + " "
	}
	return "  "
}

// cgWrapText lays text out for a region `width` columns wide, honouring the newlines already in it and
// breaking long lines at a space when there is one. Tabs are expanded HERE and not in the buffer: the
// stored text stays verbatim (it's what we send), while the painted line's visible width — which is
// what positions the cursor — accounts for them.
func cgWrapText(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n") {
		r := []rune(para)
		for len(r) > width {
			cut := width
			for i := width; i > 0; i-- {
				if r[i-1] == ' ' {
					cut = i
					break
				}
			}
			out = append(out, string(r[:cut]))
			r = r[cut:]
		}
		out = append(out, string(r))
	}
	return out
}

// cgComposeCount is the live character count. It says the LIMIT too, because the number only means
// something next to its bound, and it goes red the moment you are over — the field refuses to send
// then, and finding that out at the keystroke beats finding it out from a server 400.
func cgComposeCount(text string) string {
	n := questionLen(text)
	s := fmt.Sprintf("%s / %s characters", commaNum(n), commaNum(maxQuestionChars))
	if n > maxQuestionChars {
		return fmt.Sprintf("  %s%s — %s over%s", cgBad, s, commaNum(n-maxQuestionChars), cgOff)
	}
	return "  " + dim(s)
}

// cgComposeLines assembles the modal interior for one state of the field: the caller's body, the live
// count, then the windowed text region. Returns the body lines and the Prompt row (the region's last
// visible line, where the cursor goes). PURE — the tests lay out an 8000-character question at four
// terminal sizes and check the frame contains it, with no terminal involved.
func cgComposeLines(body []string, text string, note string, width, rows int) (lines []string, prompt string) {
	header := append([]string(nil), body...)
	if note != "" {
		header = append(header, "", "  "+cgBad+note+cgOff)
	}
	header = append(header, "", cgComposeCount(text), "")
	// cgModal.lines gives the body `rows-6-len(tail)` lines and tail is 4 here (blank, prompt, blank,
	// hint bar). On a short terminal the region wins: the count and the text are the screen's point, so
	// the caller's explanatory lines are what gets dropped.
	for len(header)+2 > rows-10 && len(header) > 1 {
		header = header[1:]
	}
	region := rows - 10 - len(header)
	if region < 1 {
		region = 1
	}

	disp := cgWrapText(text, width)
	for i := range disp {
		disp[i] = cgComposeGutter(i) + disp[i]
	}
	last := len(disp) - 1
	if len(disp) <= region+1 {
		return append(header, disp[:last]...), disp[last]
	}
	// Windowed to the END of the text — the cursor is always there, because this field only ever appends
	// or deletes at the tail. The affordance says how much is above, so a scrolled region never lies
	// about how much you have written.
	from := last - (region - 1)
	lines = append(header, "    "+dim(fmt.Sprintf("↑ %d more line(s)", from)))
	return append(lines, disp[from:last]...), disp[last]
}

// cgComposeHints is the key contract, on screen. Every one of these is reachable; nothing here is
// documented that the reader doesn't implement.
func cgComposeHints() []brand.Hint {
	return []brand.Hint{
		{Key: "⏎", Label: "newline"},
		{Key: "ctrl-d", Label: "send"},
		{Key: "ctrl-e", Label: "$EDITOR"},
		{Key: "esc", Label: "back to your session"},
	}
}

// cgComposePaint paints one state of the field and reports the modal it painted, so a test can assert
// on the same lines the terminal got.
func cgComposePaint(title string, body []string, text, note string) cgModal {
	cols, rows := cgTermSize()
	lines, prompt := cgComposeLines(body, text, note, cols-8, rows)
	m := cgModal{Title: title, Body: lines, Prompt: prompt, Hints: cgComposeHints()}
	m.paint()
	return m
}
