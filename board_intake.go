package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_intake.go — putting work ON the board without leaving it.
//
// Until this, the board could only act on work that already existed: shaping was a browser trip and
// running was a terminal one, and the split is the whole reason people kept a tab open. Filing an
// item, promoting it onto a machine and dropping it are all one keystroke from the tile now.
//
// What is deliberately NOT here: the specificity gate. `planning_finalize` refuses work that names
// no target, carries no executable acceptance check, or has open questions — and that refusal is the
// point of the gate. A quick-file from the board creates a PLANNED item at low readiness, which the
// start path then gates exactly as it gates one filed anywhere else. The board does not get its own
// weaker door into the build queue.

// newWork files a backlog item against this repo's context thread.
func (m *boardModel) newWork(c *api.Client) bool {
	thread := m.boardThread(c)
	if thread == "" {
		m.openOverlay(&noticeOverlay{heading: "no context thread here", body: wrapPlain(
			"This directory is not set up as a partyline project, so there is no thread to file work "+
				"against.\n\nRun `ptln project setup` in the repo you want to plan work for. That creates the "+
				"project, pins its thread in .partyline.json for your teammates, and registers the directory "+
				"on this machine.", 70)})
		return false
	}

	m.openOverlay(&inputOverlay{
		prompt: "new backlog item",
		hint:   "one line saying what needs to happen. Shape it further with D (describe) or in the plan.",
		onDone: func(m *boardModel, c *api.Client, title string) bool {
			id, err := c.CreateWorkItem(thread, "task", title, "", "", 0, nil)
			if err != nil {
				m.setToast("could not file it: "+err.Error(), true)
				return false
			}
			m.focusID = id // the board is about to reload; land the cursor on what was just filed
			m.setToast("filed — it is in Backlog, not started", false)
			return true
		},
	})
	return false
}

// boardThread resolves which context thread new work belongs to, using the same chain the MCP
// server uses (explicit pin, then the repo's project) rather than a second answer of its own.
func (m *boardModel) boardThread(c *api.Client) string {
	// PARTYLINE_THREAD_ID is what `ptln new --thread` sets on a session, and it wins here for the
	// same reason it wins in the MCP server: the caller was explicit.
	if t := strings.TrimSpace(os.Getenv("PARTYLINE_THREAD_ID")); t != "" {
		return t
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if t := loadRepoBind(cwd); t != "" {
		return t
	}
	remote := gitOriginURL(cwd)
	if remote == "" {
		return ""
	}
	th, _, err := c.ResolveThreadForRepo(remote, "", false)
	if err != nil || th == nil {
		return ""
	}
	return th.ID
}

// promoteItem turns a planned item into a run on a machine: pick where it builds, then dispatch.
//
// Promotion needs a machine and a project label, and the board asks for both rather than guessing.
// A wrong guess here does not fail loudly — it silently builds someone's work in the wrong place,
// which is the failure mode worth two extra keystrokes.
func (m *boardModel) promoteItem(c *api.Client, card api.BoardCard) bool {
	itemID := card.ItemID
	if itemID == "" {
		itemID = card.ID // an unscheduled card IS the work item
	}
	if card.StartBlockedByReadiness() {
		note := card.ReadinessNote
		if note == "" {
			note = "it still owes an answer before an agent could build it."
		}
		m.openOverlay(&confirmOverlay{
			prompt: "This item is under the readiness floor: " + note +
				"\n\nStarting it anyway dispatches a half-specified task. Continue?",
			onYes: func(m *boardModel, c *api.Client) bool { return m.pickMachineFor(c, itemID) },
		})
		return false
	}
	return m.pickMachineFor(c, itemID)
}

// pickMachineFor asks which machine builds this, then which of its projects.
func (m *boardModel) pickMachineFor(c *api.Client, itemID string) bool {
	machines, err := c.MachineOffers()
	if err != nil {
		m.setToast("could not read your machines: "+err.Error(), true)
		return false
	}
	var items []pickerItem
	for _, mo := range machines {
		note := fmt.Sprintf("%d project(s)", len(mo.Repos))
		if !mo.Online {
			note += " · offline"
		}
		items = append(items, pickerItem{Label: mo.Machine, Note: note, Value: mo.DaemonID})
	}
	if len(items) == 0 {
		m.openOverlay(&noticeOverlay{heading: "no machines", body: wrapPlain(
			"No machine is advertising a project to build in. Run `ptln daemon add-project <label> <dir>` "+
				"on the box you want work to run on — that registers the directory and makes it a target.", 70)})
		return false
	}

	m.openOverlay(&pickerOverlay{
		heading: "build it on which machine?",
		items:   items,
		onPick: func(m *boardModel, c *api.Client, machine pickerItem) bool {
			return m.pickProjectFor(c, itemID, machine, machines)
		},
	})
	return false
}

func (m *boardModel) pickProjectFor(c *api.Client, itemID string, machine pickerItem, machines []api.MachineOffer) bool {
	var labels []pickerItem
	for _, mo := range machines {
		if mo.DaemonID != machine.Value {
			continue
		}
		for _, r := range mo.Repos {
			labels = append(labels, pickerItem{Label: r.Name, Note: r.Parent, Value: r.Name})
		}
	}
	if len(labels) == 0 {
		m.setToast(machine.Label+" advertises no projects — add one with `ptln daemon add-project`", true)
		return false
	}
	if len(labels) == 1 {
		return m.doPromote(c, itemID, machine.Value, labels[0].Value)
	}
	m.openOverlay(&pickerOverlay{
		heading: "which project on " + machine.Label + "?",
		items:   labels,
		onPick: func(m *boardModel, c *api.Client, proj pickerItem) bool {
			return m.doPromote(c, itemID, machine.Value, proj.Value)
		},
	})
	return false
}

func (m *boardModel) doPromote(c *api.Client, itemID, daemonID, label string) bool {
	id, err := c.PromoteWorkItem(itemID, daemonID, label, "", false)
	if err != nil {
		m.setToast("promote refused: "+err.Error(), true)
		return true
	}
	m.focusID = id
	m.setToast("promoted — it is queued on "+label, false)
	return true
}

// deleteItem removes a planned item. Only ever offered on UNSCHEDULED cards: a card with a run
// behind it is discarded (which archives the run and keeps its git history), never deleted.
func (m *boardModel) deleteItem(c *api.Client, card api.BoardCard) bool {
	id := card.ItemID
	if id == "" {
		id = card.ID
	}
	if err := c.DeleteWorkItem(id); err != nil {
		m.setToast("could not delete it: "+err.Error(), true)
		return true
	}
	m.setToast("deleted", false)
	return true
}

// describeWork hands off to the describe interview — the agent-led path from "here is a problem" to
// a specified, buildable item.
//
// It hands off rather than embedding: describe is a multi-turn conversation with an engine, and
// re-implementing that inside a modal would be a second, worse copy of a flow that already exists.
// Inside the ptln tmux it opens as its own window, so the board stays where it is; otherwise the
// board steps aside and returns you to it afterwards.
func (m *boardModel) describeWork(c *api.Client) bool {
	thread := m.boardThread(c)
	args := []string{"describe"}
	if thread != "" {
		args = append(args, "--thread", thread)
	}
	self := selfExe()

	if insidePtlnTmux() {
		cmd := tmuxCmd(append([]string{"new-window", "-n", "describe", self}, args...)...)
		if err := cmd.Run(); err != nil {
			m.setToast("could not open the describe window: "+err.Error(), true)
			return false
		}
		m.setToast("describe opened in a new window", false)
		return false
	}

	m.handOff = func() { boardExec(exec.Command(self, args...)) }
	m.quitAfterKey = true
	return false
}

// reorder moves a Backlog card up or down the queue by rewriting its rank.
//
// Backlog order is a decision somebody made about what happens next, and it was the one thing the
// board could show but not change — the action menu even named a shortcut that did nothing. Rank is
// strictly descending, so moving a card means taking a rank between its new neighbours; landing on
// the end just steps past the last one.
func (m *boardModel) reorder(c *api.Client, dir int) bool {
	if m.focusedColumn() != api.ColBacklog {
		m.setToast("only the Backlog is ordered — the other columns follow what happened", false)
		return false
	}
	card, ok := m.focused()
	if !ok {
		return false
	}

	cards := sortColumn(api.ColBacklog, m.data.Column(api.ColBacklog))
	at := -1
	for i := range cards {
		if cards[i].ID == card.ID {
			at = i
			break
		}
	}
	if at < 0 {
		return false
	}
	to := at + dir
	if to < 0 || to >= len(cards) {
		return false // already at the end; nothing to swap with
	}

	// The rank that puts it between its new neighbours. At either end there is only one neighbour,
	// so step a whole unit past it rather than averaging against a rank that does not exist.
	var rank float64
	switch {
	case dir < 0 && to == 0:
		rank = cards[0].Rank + 1
	case dir > 0 && to == len(cards)-1:
		rank = cards[len(cards)-1].Rank - 1
	case dir < 0:
		rank = (cards[to-1].Rank + cards[to].Rank) / 2
	default:
		rank = (cards[to].Rank + cards[to+1].Rank) / 2
	}

	if err := c.SetRunRank(card.ID, rank); err != nil {
		m.setToast("could not reorder: "+err.Error(), true)
		return false
	}
	m.focusID = card.ID // follow the card to its new position
	return true
}
