package main

import (
	"time"

	"charm.land/bubbles/v2/viewport"
)

func contactLauncher(render contactRender, now time.Time) {
	all := []aiSession{
		{ID: "csv-export", Tool: "claude", Cwd: "/projects/alpha", Title: "Export tickets as CSV", LastActive: now.Add(-2 * time.Hour)},
		{ID: "test-review", Tool: "codex", Cwd: "/projects/alpha", Title: "Review tests", LastActive: now.Add(-time.Hour)},
	}
	for _, s := range all {
		detailCache[s.ID] = &aiDetail{Branch: "main", First: "Export tickets as CSV.", Model: "fixture", Messages: 8}
	}
	for _, state := range []string{"list", "preview", "search", "rename", "delete confirm", "permission picker", "permission armed", "custom flags", "help", "flash", "marked", "empty"} {
		a := &aiMenu{all: all, meta: map[string]sessMeta{}, w: 80, h: 24, now: func() time.Time { return now }}
		a.collapsed = map[string]bool{"/projects/alpha": false}
		a.applyFilter()
		a.cursor = 1
		switch state {
		case "list":
			a.cursor = 0
		case "preview":
			a.focusR = true
		case "search":
			a.filter, a.query = true, "Export"
			a.applyFilter()
		case "rename":
			a.renaming, a.renameBuf = true, "CSV export"
		case "delete confirm":
			cp := *a.selected()
			a.confirmDelete = &cp
			a.delFacts = deleteFacts{Branch: "main", Dirty: 3, Age: "2h", Recent: true, TrashUnder: "~/.partyline/trash"}
		case "permission picker", "permission armed", "custom flags":
			a.picking, a.pickModes = true, permissionModes(a.selected().Tool) // the selected session's own engine
			if state == "permission armed" {
				for i, mode := range a.pickModes {
					if mode.danger {
						a.pickIdx, a.pickArmed = i, true
						break
					}
				}
			}
			if state == "custom flags" {
				a.pickCustom, a.pickText = true, "--verbose"
			}
		case "help":
			a.help = true
		case "flash":
			a.flash = "Session pinned"
		case "marked":
			a.marked = map[string]bool{"csv-export": true}
		case "empty":
			a.query = "no match"
			a.applyFilter()
		}
		render("launcher popup / "+state, launcherPopup{menu: a})
	}
}

func contactLoops(render contactRender, now time.Time) {
	rows := []tabRow{
		{Section: tabNeedsYou, RunID: "a1b2c3", Repo: "alpha", Title: "CSV export", State: loopAwaiting, Detail: "brief waiting for approval", Age: 2 * time.Minute},
		{Section: tabInReview, RunID: "d4e5f6", Repo: "alpha", Title: "Review tests", State: loopChecking, Detail: "checks done · codex reading", Age: 8 * time.Minute},
		{Section: tabInProgress, RunID: "0a0b0c", Repo: "beta", Title: "Build exporter", State: loopBuilding, Age: time.Hour},
		{Section: tabInProgress, RunID: "1c2d3e", Repo: "gamma", Title: "Batch step 2", State: loopBuilding, Batch: "step 2 of 4", Watch: "4e5f6a7b8c9d0e", Detail: "⏸ resumes 15:40 · every builder at its limit · round 1 of 6", Age: 20 * time.Minute,
			Round: 1, Builder: "codex:gpt-5", Reset: now.Add(40 * time.Minute)},
		{Section: tabInProgress, RunID: "2d3e4f", Repo: "gamma", Title: "Batch step 3", State: loopBuilding, Batch: "step 3 of 4", Watch: "5f6a7b8c9d0e1f", Age: 50 * time.Minute,
			Round: 2, Builder: "claude:opus"},
		{Section: tabPlanned, Repo: "beta", Title: "Document exporter", Batch: "step 4 of 5"},
		{Section: tabDone, RunID: "f70a83", Repo: "alpha", Title: "Add fixtures", State: loopPRReady, Age: 3 * time.Hour},
	}
	m := newTabModel(tabActions{rows: func(time.Time) []tabRow { return rows }, now: func() time.Time { return now }})
	render("Loops board / list", m)
	m.detail, m.vp = true, viewport.New(viewport.WithWidth(76), viewport.WithHeight(18))
	m.vp.SetContent("Round 1\nChecks: passed\nReview: codex done · no open P1\n\nRound 2\nChecks: passed\nReview: claude reading")
	render("Loops board / detail", m)
	m.detail, m.rows = false, nil
	render("Loops board / empty welcome", m)
	m.contractors = true
	m.crew = []*contractor{{ID: "fixture", Loop: "a1b2c3", Role: "builder", Engine: "codex", Title: "Build CSV export", State: "running", Started: now.Add(-time.Minute)}}
	render("Loops / Contractors", m)
}
