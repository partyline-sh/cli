// Deleting a whole project from the launcher: `x` on a project header.
//
// A list full of scratch projects (tmp, 002, tes2…) used to take one `x` and one confirmation per
// session. On a header, `x` now gathers every session shown under it and asks once. It uses the
// same deleteSession path as a single delete, so each session's files MOVE to ~/.partyline/trash
// with a manifest; nothing is unlinked. Running sessions and tools that share one store across
// sessions are left in place and counted in the confirmation, never moved.
package main

import (
	"fmt"
	"os"
	"time"

	"partyline.sh/partyline/internal/brand"
	uitheme "partyline.sh/partyline/internal/theme"
)

// bulkDelete is what a project-header delete will move, gathered before the confirmation opens.
type bulkDelete struct {
	label    string
	sessions []aiSession // closed sessions with their own files, in display order
	recent   int         // of those, used within the last day
	live     int         // running sessions, left in place
	shared   int         // sessions of tools with one shared store, left in place
	gone     bool        // the project's directory no longer exists
}

// confirming reports whether a delete confirmation (single or project) owns input.
func (m *aiMenu) confirming() bool { return m.confirmDelete != nil || m.confirmBulk != nil }

// bulkFor collects the sessions shown under a header row. A worktree-group header takes only the
// worktree sessions; a project header takes everything in the project.
func (m *aiMenu) bulkFor(r treeRow) bulkDelete {
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	b := bulkDelete{label: projLabel(r.proj)}
	if r.group {
		b.label += " ⤷ worktrees"
	}
	if r.proj != "" {
		if _, err := os.Stat(r.proj); err != nil {
			b.gone = true
		}
	}
	for _, s := range m.view {
		if s.projKey() != r.proj || (r.group && s.WtName == "") {
			continue
		}
		if s.Live || m.live[s.ID] { // open in tmux counts, even when idle
			b.live++
			continue
		}
		if _, ok := sessionFiles(s); !ok {
			b.shared++
			continue
		}
		if !s.LastActive.IsZero() && now.Sub(s.LastActive) < 24*time.Hour {
			b.recent++
		}
		b.sessions = append(b.sessions, s)
	}
	return b
}

// openBulkDelete handles `x` on a header: it opens the confirmation, or explains why there is
// nothing to move.
func (m *aiMenu) openBulkDelete(r treeRow) {
	b := m.bulkFor(r)
	if len(b.sessions) == 0 {
		m.flash = "nothing to delete in " + b.label
		if b.live > 0 {
			m.flash += fmt.Sprintf(" — %d running, close them first", b.live)
		}
		return
	}
	m.confirmBulk = &b
}

// handleConfirmBulk drives the project confirmation. [y]/⏎ moves every gathered session to the
// trash; anything else cancels.
func (m *aiMenu) handleConfirmBulk(key []byte) (bool, *aiSession) {
	b := m.confirmBulk
	m.confirmBulk = nil
	if c := key[0]; c != 'y' && c != 'Y' && c != '\r' && c != '\n' {
		m.flash = "cancelled — nothing deleted"
		return false, nil
	}
	done, failed := 0, 0
	for _, s := range b.sessions {
		if _, err := launcherMenuDelete(s, m.meta); err != nil {
			failed++
			continue
		}
		done++
	}
	m.refresh() // the files are gone, so re-scanning the stores is what drops the rows
	m.flash = fmt.Sprintf("✓ deleted %d session(s) · recoverable from %s", done, sessionTrashDir())
	if failed > 0 {
		m.flash += fmt.Sprintf(" · %d could not be moved and are untouched", failed)
	}
	return false, nil
}

// bulkDeleteModalLines renders the project confirmation: how many move, and the reasons not to.
func bulkDeleteModalLines(b bulkDelete) []string {
	grey := func(t string) string { return tmuxTheme().LegacyForeground(uitheme.LegacyMuted) + t + "\x1b[0m" }
	warn := func(t string) string {
		return "\x1b[1m" + tmuxTheme().LegacyForeground(uitheme.LegacyAccent) + t + "\x1b[0m"
	}
	lines := []string{
		"\x1b[1m" + tmuxTheme().LegacyForeground(uitheme.LegacyText) + brand.Clip(b.label, 46) + "\x1b[0m",
		grey(fmt.Sprintf("%d session(s) move to the trash", len(b.sessions))),
		"",
	}
	if b.gone {
		lines = append(lines, grey("✗ its directory no longer exists"))
	}
	if b.recent > 0 {
		lines = append(lines, warn(fmt.Sprintf("⚠ %d used in the last day", b.recent)))
	}
	if b.live > 0 {
		lines = append(lines, warn(fmt.Sprintf("⚠ %d running — left in place", b.live)))
	}
	if b.shared > 0 {
		lines = append(lines, grey(fmt.Sprintf("%d share one store — left in place", b.shared)))
	}
	return append(lines, "",
		grey("recoverable from "+brand.Clip(sessionTrashDir(), 46)),
		"",
		"\x1b[1m"+tmuxTheme().LegacyForeground(uitheme.LegacyDanger)+"[y] delete all\x1b[0m   "+tmuxTheme().LegacyForeground(uitheme.LegacyFaint)+"[n] cancel\x1b[0m")
}
