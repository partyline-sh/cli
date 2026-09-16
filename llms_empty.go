package main

// "Didn't find any sessions — where should I look?"
//
// WHY THIS IS A MENU AND NOT A CONFIG KEY. The failure it addresses is discovery, not capability. A
// `session_roots` setting would fix the problem for anyone who already knew the setting existed —
// which is nobody, because you do not go reading the configuration reference for a tool that
// appears to be working right up until the moment it visibly is not.
//
// So the question gets asked at the ONE moment the user is guaranteed to be receptive: they opened
// the session list looking for a session, and it is not there. That is the highest-signal instant
// this program will ever have, and until now it rendered an empty list and said nothing.
//
// WHY IT OFFERS RATHER THAN ADOPTS. Detection finds other homes on this machine holding readable
// session stores. Readable is the whole test — the OS has already decided what this process may
// see, so there is no second privacy judgement for partyline to make. But on a shared box a
// readable home might belong to a colleague, so the found roots are presented as CHOICES. The user
// says which one is theirs; nothing is adopted silently.
//
// It uses cgBox/cgRow/menuKey like every other modal in the mux — same frame, same key colours,
// same single-keypress feel, same q/esc-cancels. A one-off screen that merely LOOKS adjacent to the
// rest is how a UI stops feeling like one program.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// emptyChoice is one row of the modal: the key that selects it, what it says, and what it means.
//
// ONE SOURCE OF TRUTH for drawn-and-handled. The first version built the row list and the key
// switch separately, which makes "the menu shows 3 but pressing 3 does nothing" a one-line edit
// away at all times — and it is invisible to every test that does not actually press keys. Deriving
// both from this slice makes that class of bug unrepresentable rather than merely unlikely.
type emptyChoice struct {
	Key   rune
	Label string
	Note  string
	Root  string // the home to adopt; "" means "prompt for a typed path"
}

// emptyChoices builds the menu for a set of detected roots.
//
// Candidates are capped at nine because the keys are 1-9 and a tenth row would be drawn with no way
// to select it — a menu item you can see and cannot press is worse than one that is not offered.
func emptyChoices(found []string) []emptyChoice {
	out := make([]emptyChoice, 0, len(found)+2)
	for i, p := range found {
		if i >= 9 {
			break
		}
		out = append(out, emptyChoice{Key: rune('1' + i), Label: shortHome(p), Root: p})
	}
	out = append(out,
		emptyChoice{Key: 'p', Label: "type a path", Note: "if it isn't listed"},
		emptyChoice{Key: 'q', Label: "not now"},
	)
	return out
}

// emptyStateLines renders the modal body. Pure, so a test can inspect exactly what a user sees
// without a terminal (cgPaintLines is pure for the same reason).
func emptyStateLines(found []string, choices []emptyChoice) []string {
	lines := []string{
		fmt.Sprintf("  %sNo AI sessions found.%s", cgBold, cgOff),
		"",
		fmt.Sprintf("  %sSessions live under a home directory. This one looks in%s", cgDim, cgOff),
		fmt.Sprintf("  %s%s%s", cgDim, currentHome(), cgOff),
		fmt.Sprintf("  %sA session started with a different HOME — after a migration,%s", cgDim, cgOff),
		fmt.Sprintf("  %sor under another account — is invisible from here.%s", cgDim, cgOff),
		"",
	}
	if len(found) > 0 {
		lines = append(lines, fmt.Sprintf("  %sother homes on this machine holding sessions%s", cgDim, cgOff))
	}
	for i, c := range choices {
		// A blank line between the detected roots and the always-present actions, so the two groups
		// read as different kinds of thing rather than one long list.
		if c.Root == "" && i > 0 && choices[i-1].Root != "" {
			lines = append(lines, "")
		}
		lines = append(lines, cgRow(string(c.Key), c.Label, c.Note))
	}
	return lines
}

// offerSessionRoots runs the empty-state modal. Returns true when a root was adopted, so the caller
// re-scans instead of redrawing the same empty list.
func offerSessionRoots() bool {
	found := candidateRoots()
	choices := emptyChoices(found)
	cgBox("Where should I look?", emptyStateLines(found, choices))

	k := menuKey()
	for _, c := range choices {
		if c.Key != k || c.Key == 'q' {
			continue
		}
		if c.Root != "" {
			return adoptRoot(c.Root)
		}
		fmt.Println()
		p, ok := Input("path to the home directory", "")
		if !ok {
			return false
		}
		return adoptRoot(strings.TrimSpace(p))
	}
	return false // q, esc, enter, or anything unrecognised
}

// adoptRoot validates and persists one root, and says what it did.
//
// The validation is not ceremony: a path with no store under it is far likelier to be a typo — or
// the PROJECT directory rather than the home — than a reason to remember it forever. Silently
// accepting one leaves the user believing the problem is solved when nothing changed.
func adoptRoot(path string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(expandTilde(path))
	if err != nil {
		return false
	}
	if !hasSessionStore(abs) {
		cgBox("Nothing there", []string{
			fmt.Sprintf("  %s%s%s", cgBad, abs, cgOff),
			"",
			fmt.Sprintf("  %sholds no session store. Expected one of%s", cgDim, cgOff),
			fmt.Sprintf("  %s.claude/projects · .codex/sessions · .gemini/tmp%s", cgDim, cgOff),
			fmt.Sprintf("  %sinside it — the HOME directory, not the project dir.%s", cgDim, cgOff),
			"",
			cgRow("y", "add it anyway", ""),
			cgRow("q", "cancel", ""),
		})
		if k := menuKey(); k != 'y' {
			return false
		}
	}
	if err := addSessionRoot(abs); err != nil {
		cgBox("Couldn't save that", []string{fmt.Sprintf("  %s%v%s", cgBad, err, cgOff)})
		_ = menuKey()
		return false
	}
	cgBox("Added", []string{
		fmt.Sprintf("  %s✓%s looking in %s", cgOK, cgOff, shortHome(abs)),
		"",
		// Said once, at adoption: this is what makes those sessions resumable at all, and it also
		// means they run against THAT home's tool config rather than this one's.
		fmt.Sprintf("  %ssessions from there resume with HOME set to it%s", cgDim, cgOff),
	})
	_ = menuKey()
	return true
}

// shortHome renders a home path for a menu row: ~ for your own, and MIDDLE-elided when it is too
// long for the frame.
//
// Elided in the middle rather than clipped at the end, which is what the frame would otherwise do.
// For a path the tail is the identifying part — "…/nested/deeper" tells you which home this is,
// while the frame's right-clip would leave you staring at a prefix every candidate shares. Caught
// by a width test, not by looking at it.
func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && p == home {
		return "~"
	}
	const max = 52 // leaves room for the key, the gutters, and the frame at 80 cols
	if len(p) <= max {
		return p
	}
	head, tail := p[:max/2-1], p[len(p)-(max/2-2):]
	return head + "…" + tail
}

func currentHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "your home directory"
	}
	return h
}
