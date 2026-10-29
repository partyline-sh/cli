package main

import "fmt"

func contactForms(render contactRender) {
	render("popup form / choice", newRunForm())
	render("popup form / worktree choice", worktreeForm())
	render("popup form / pick", formModel{kind: formPick, title: "Switch thread", label: "attach", items: []string{"CSV export", "Review tests"}})
	items := make([]string, 30)
	for i := range items {
		items[i] = fmt.Sprintf("thread %02d", i+1)
	}
	render("popup form / long pick", formModel{kind: formPick, title: "Switch thread", label: "attach", items: items, off: 20, typed: "24"})
	render("popup form / ask", formModel{kind: formAsk, title: "Fork into worktree", label: "worktree name", def: "feature", typed: "csv-export"})
	render("popup form / confirm", formModel{kind: formConfirm, title: "Remove a worktree", label: "delete the worktree directory?"})
	render("popup form / note", formModel{kind: formNote, title: "Worktrees", body: []string{"feature  /workspace/feature", "fix      /workspace/fix"}})
}

func contactScreens(render contactRender) {
	doors := []welcomeDoor{{disp: "↵", key: '\r', title: "resume last session"}, {disp: "n", key: 'n', title: "new session"}, {disp: "/", key: '/', title: "find a session"}}
	for i := range doors {
		render(fmt.Sprintf("welcome / door %d", i+1), welcomeModel{doors: doors, sel: i, result: -1})
	}
	adopt := emptyStateForm([]string{"/home/old"})
	adopt.body[3] = "/home/partyline"
	render("empty state / adopt a home", adopt)
	empty := emptyStateForm(nil)
	empty.body[3] = "/home/partyline"
	render("empty state / no homes", empty)
	render("recover / recorded remote", recoverForm("/work/gone", "https://example.org/repo"))
	render("recover / no remote", recoverForm("/work/gone", ""))
	for _, total := range []int{0, 4} {
		render(fmt.Sprintf("boot splash / total %d", total), bootScreen{model: bootModel{Elapsed: bootThreshold, Total: total, Steps: []bootStep{{State: bootDone}}}})
	}
	render("boot splash / working", bootScreen{model: bootModel{Elapsed: 2 * bootThreshold, Total: 4, Steps: []bootStep{{State: bootDone}, {State: bootRunning, Label: "Restore sessions", Detail: "CSV export"}}}})
	render("boot splash / completed", bootScreen{model: bootModel{Elapsed: 2 * bootThreshold, Total: 1, Steps: []bootStep{{State: bootDone}}}})
	render("boot splash / before threshold", bootScreen{model: bootModel{Threshold: bootThreshold}})
}
