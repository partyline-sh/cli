package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ansix "github.com/charmbracelet/x/ansi"
)

// contactSheet renders real views without starting programs or running their commands. A private
// home isolates theme, watcher and brief reads from the person's state. Callers must serialize
// it with other code that changes the environment (as the golden tests already do).
func contactSheet() (string, error) {
	home, err := os.MkdirTemp("", "ptln-contact-sheet-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	old, had := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		return "", err
	}
	defer func() {
		if had {
			_ = os.Setenv("HOME", old)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}()
	oldCache := detailCache
	detailCache = map[string]*aiDetail{}
	defer func() { detailCache = oldCache }()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	// Times a screen shows in local time (a limit's reset) read the same on every machine.
	oldLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = oldLocal }()
	var out strings.Builder
	out.WriteString("Partyline dark · 80x24 · fixed fixtures, 2026-10-27 12:00 UTC\n")
	var tooTall []string
	render := func(name string, m tea.Model) {
		m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		fmt.Fprintf(&out, "\n=== %s ===\n", name)
		lines := strings.Split(ansix.Strip(m.View().Content), "\n")
		// A screen taller than its window loses its top or bottom in a real terminal: an error
		// here, rather than a sheet that quietly shows the first 24 rows.
		if len(lines) > 24 {
			tooTall = append(tooTall, fmt.Sprintf("%s (%d rows)", name, len(lines)))
		}
		// Record the visible 80x24 viewport, including empty rows, as a terminal would.
		for i := 0; i < 24; i++ {
			line := ""
			if i < len(lines) {
				line = ansix.Truncate(lines[i], 80, "")
			}
			out.WriteString(strings.TrimRight(line, " ") + "\n")
		}
	}
	contactMenu(render)
	if err := contactApprovals(render); err != nil {
		return "", err
	}
	contactLoops(render, now)
	contactChat(render)
	contactForms(render)
	contactLauncher(render, now)
	contactScreens(render)
	if len(tooTall) > 0 {
		return "", fmt.Errorf("taller than 80x24: %s", strings.Join(tooTall, ", "))
	}
	return out.String(), nil
}

// contactSheetMain prints by default; -o writes the same bytes to a requested file.
func contactSheetMain(args []string) {
	if len(args) != 0 && (len(args) != 2 || args[0] != "-o") {
		fatal(fmt.Errorf("usage: ptln _contact-sheet [-o docs/contact-sheet.txt]"))
	}
	sheet, err := contactSheet()
	if err != nil {
		fatal(err)
	}
	if len(args) == 2 {
		if err := os.WriteFile(args[1], []byte(sheet), 0o644); err != nil {
			fatal(err)
		}
	} else {
		fmt.Print(sheet)
	}
}

type contactRender func(string, tea.Model)

func contactMenu(render contactRender) {
	items := []tmuxMenuItem{{label: "Build CSV export", key: "1", num: "1", paneID: "%1", status: "waiting"}, {label: "Review tests", key: "2", num: "2", paneID: "%2", status: "active"}}
	items = append(items, tmuxCommandItems("ptln")...)
	m := newMenuModel(items, "%1", menuActions{})
	render("ctrl-\\ menu / list", m)
	m.filtering, m.filter = true, "export"
	m = m.refilter()
	render("ctrl-\\ menu / filter", m)
	m.filter = "missing"
	m = m.refilter()
	render("ctrl-\\ menu / no match", m)
	m.filtering, m.filter = false, ""
	m = m.refilter()
	m.tab, m.sel = 1, m.commandSel
	render("ctrl-\\ menu / commands", m)
	for i, it := range items {
		if it.confirm {
			m.sel, m.armed = i, i
			render("ctrl-\\ menu / confirm "+it.key, m)
		}
	}
	m.armed, m.note = -1, "Action refused."
	render("ctrl-\\ menu / refusal", m)
}

func contactChat(render contactRender) {
	m := chatPopup{in: newChatInput([]string{"check the build"})}
	m = chatLayoutFixture(m)
	m.in.ed.Set("thanks")
	m.in.status = []string{"codex-1 working · 2m"}
	render("chat popup / conversation", m)
	m.in.searching, m.in.searchAt = true, 0
	render("chat popup / history search", m)
	m.in.searching, m.pinned = false, true
	render("chat popup / pinned tab", m)
}

// chatLayoutFixture keeps the golden and contact sheet on the same conversation.
func chatLayoutFixture(m chatPopup) chatPopup {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local)
	for _, msg := range []chatMessage{
		{Seq: 1, From: "you", Name: "you", Text: "can someone check the build?", At: at},
		{Seq: 2, From: "codex", Name: "codex-1", Text: "on it", At: at},
	} {
		m = m.showNew([]chatMessage{msg}, 0)
	}
	return m
}
