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

// offerSessionRoots runs the empty-state modal. Returns true when a root was adopted, so the caller
// re-scans instead of redrawing the same empty list.
func offerSessionRoots() bool {
	found := candidateRoots()

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
		for i, p := range found {
			lines = append(lines, cgRow(fmt.Sprint(i+1), shortHome(p), ""))
		}
		lines = append(lines, "")
	}
	typedKey := "p"
	lines = append(lines,
		cgRow(typedKey, "type a path", "if it isn't listed"),
		cgRow("q", "not now", ""),
	)
	cgBox("Where should I look?", lines)

	k := menuKey()
	switch {
	case k == 'q' || k == 0 || k == '\r' || k == 27:
		return false
	case k >= '1' && k <= '9':
		if i := int(k - '1'); i < len(found) {
			return adoptRoot(found[i])
		}
		return false
	case k == rune(typedKey[0]):
		fmt.Println()
		p, ok := Input("path to the home directory", "")
		if !ok {
			return false
		}
		return adoptRoot(strings.TrimSpace(p))
	}
	return false
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

// shortHome renders a home path the way the rest of the UI does — ~ for your own, plain otherwise.
func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if p == home {
			return "~"
		}
		if parent := filepath.Dir(home); filepath.Dir(p) == parent {
			return p // a sibling home reads better in full than abbreviated
		}
	}
	return p
}

func currentHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "your home directory"
	}
	return h
}
