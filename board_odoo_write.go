package main

// board_odoo_write.go — the write half of the Odoo board: move a task between stages, retitle
// it, post an internal note.
//
// board_odoo.go opens with "read-only, permanently and by design", and the reasoning there —
// don't invent a second path to production data — still shapes HOW this writes, even though the
// operator has since asked for the board to manage the backlog outright. Three lines hold:
//
//	1. Every write goes through the SERVER'S OWN curated tools, under the server's own
//	   credentials and validation. `update_project_task` refuses a stage that isn't the task's
//	   project's; `post_note` escapes its body and cannot email a customer. partyline never
//	   speaks raw ORM: there is no generic `write` here, deliberately, because a curated tool's
//	   guarantees are the whole difference between a gesture and a foot-gun.
//	2. Which tools, exactly, is CONFIG — `update_tool` and `comment_tool` in the catalog's board
//	   opts — with defaults matching the tool names this integration's server actually exposes.
//	   A server without them stays read-only with an honest message (the tool-missing error
//	   surfaces in the toast), which keeps read-only as the default posture for any other Odoo.
//	3. Writes act on TASKS. The projects overview is a board of containers; requireTaskScope
//	   sends the operator into a project rather than letting a write land on the wrong model.
//
// Args are assembled by pure builders so tests can pin the exact payload each gesture sends.

import (
	"fmt"
	"strconv"

	"partyline.sh/partyline/internal/api"
)

// odooUpdateArgs is the payload for a task update: id always, plus only the fields to change —
// the tool leaves everything omitted untouched, which is what makes small edits safe.
func odooUpdateArgs(taskID string, stageID string, title string) (map[string]any, error) {
	id, err := strconv.Atoi(taskID)
	if err != nil {
		return nil, fmt.Errorf("%q is not an Odoo task id", taskID)
	}
	args := map[string]any{"id": id}
	if stageID != "" {
		sid, err := strconv.Atoi(stageID)
		if err != nil {
			return nil, fmt.Errorf("%q is not an Odoo stage id", stageID)
		}
		args["stage_id"] = sid
	}
	if title != "" {
		args["name"] = title
	}
	return args, nil
}

// odooNoteArgs is the payload for an internal note on a task.
func odooNoteArgs(taskID, body string) (map[string]any, error) {
	id, err := strconv.Atoi(taskID)
	if err != nil {
		return nil, fmt.Errorf("%q is not an Odoo task id", taskID)
	}
	return map[string]any{"model": "project.task", "id": id, "body": body}, nil
}

// MoveCard moves a task to another stage of its own project. The column key IS the stage id —
// the same id the board's columns were built from, so the destination can only be a stage this
// project declared.
func (s odooSource) MoveCard(scope, cardID string, to api.BoardColumn) error {
	if err := requireTaskScope(scope); err != nil {
		return err
	}
	args, err := odooUpdateArgs(cardID, string(to), "")
	if err != nil {
		return err
	}
	_, err = s.p.callTool(s.cfg.Opt("update_tool", "update_project_task"), args)
	return err
}

// EditTitle replaces a task's name. Nothing else rides along — see boardCardEditor for why
// descriptions are out of a one-line prompt's reach.
func (s odooSource) EditTitle(scope, cardID, title string) error {
	if err := requireTaskScope(scope); err != nil {
		return err
	}
	args, err := odooUpdateArgs(cardID, "", title)
	if err != nil {
		return err
	}
	_, err = s.p.callTool(s.cfg.Opt("update_tool", "update_project_task"), args)
	return err
}

// CommentCard posts an internal note to the task's chatter — append-only, staff-visible, no
// email. The board's detail pane re-reads the chatter on the refresh that follows, so the note
// appears where the conversation already renders.
func (s odooSource) CommentCard(scope, cardID, body string) error {
	if err := requireTaskScope(scope); err != nil {
		return err
	}
	args, err := odooNoteArgs(cardID, body)
	if err != nil {
		return err
	}
	_, err = s.p.callTool(s.cfg.Opt("comment_tool", "post_note"), args)
	return err
}

// odooSeqArgs is the payload for a row-order write: the task and its new kanban sequence.
func odooSeqArgs(taskID string, seq int) (map[string]any, error) {
	id, err := strconv.Atoi(taskID)
	if err != nil {
		return nil, fmt.Errorf("%q is not an Odoo task id", taskID)
	}
	return map[string]any{"id": id, "sequence": seq}, nil
}

// SetCardSeq writes one task's kanban sequence — the row-order half of the carry gesture.
// Needs the curated update tool to accept `sequence`; a server that doesn't refuses cleanly
// and the toast carries its refusal.
func (s odooSource) SetCardSeq(scope, cardID string, seq int) error {
	if err := requireTaskScope(scope); err != nil {
		return err
	}
	args, err := odooSeqArgs(cardID, seq)
	if err != nil {
		return err
	}
	_, err = s.p.callTool(s.cfg.Opt("update_tool", "update_project_task"), args)
	return err
}
