package main

import (
	"os"
	"path/filepath"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
)

func contactApprovals(render contactRender) error {
	r := &loopRun{ID: "a1b2c3", Repo: "/projects/alpha", State: loopAwaiting, Revision: 1, Checks: []string{"go test ./..."}, Review: "claude,codex", Round: 1, Note: "building CSV export"}
	brief := "# CSV export\n\n## Goal\nExport tickets as CSV.\n\n## Scope\nAdd a CSV exporter with tests.\n\n## Batch review\nBoth reviewers agree; no open P1.\n"
	if err := os.MkdirAll(filepath.Dir(loopFile(r.ID, "brief-1.md")), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(brief), 0o600); err != nil {
		return err
	}
	in := gateInfo{goal: "Export tickets as CSV.", tab: "tab 1 claude", revision: "first version", dispute: loopTracked{ID: "F1-1", loopFinding: loopFinding{Priority: "P1", File: "export.go", Line: 12, Quote: "write(row)", Claim: "The error is ignored.", Fix: "Return the write error."}, Engine: "codex", Dispute: "The caller checks the stream error."}}
	m := gateModel{runs: []*loopRun{r}, info: map[string]gateInfo{r.ID: in}, list: true, note: textinput.New(), vp: viewport.New()}
	render("approval popup / list", m)
	m.list = false
	render("approval popup / brief", m)
	m.mode = gateDetails
	m.vp.SetContent(brief)
	render("approval popup / full brief", m)
	m.mode, m.code, m.label, m.typed, m.blink = gateConfirm, "4217", "approve loop a1b2c3", "42", true
	render("approval popup / confirm", m)
	m.mode, m.done, m.doneOn = gateDone, "approved", "loop a1b2c3"
	render("approval popup / done", m)
	m.mode = gateNote
	m.note.SetValue("Add a failure-path test.")
	render("approval popup / reject note", m)
	m.mode, r.State = gateSummary, loopAwaitYou
	render("approval popup / dispute", m)
	b := &loopBatch{ID: "b4c5d6", Briefs: []string{brief}, Runs: []string{r.ID}}
	if err := b.save(); err != nil {
		return err
	}
	r.State, r.Batch, r.Step = loopAwaiting, b.ID, 1
	render("approval popup / review", m)
	r.Batch, r.Step = "", 0
	r.State = loopChecking
	render("approval popup / running review", m)
	r.State = loopBuilding
	render("approval popup / running run", m)
	m.runs = nil
	render("approval popup / empty", m)
	return nil
}
