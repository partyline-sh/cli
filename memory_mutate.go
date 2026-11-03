package main

// EDITING FACTS IN PLACE.
//
// The original store was append-only, and that is what broke it. Two facts on the live server, both
// active at once:
//
//	#30  failed events "go to the dead-letter queue"
//	#141 "Correction … quarantined into the Rejected list, NOT the dead-letter queue"
//
// Both briefed to the agent, nothing saying which wins. A correction could only ever be another
// fact, so the wrong answer never died. 4 of 146 facts read as corrections; 7 had ever set
// `supersedes`.
//
// So a fact can be rewritten. The claim is replaced, not annotated, and the thing a later reader
// gets is what is true. Nothing is lost, because the facts are files in the repo: `git log -p
// memory/` is every edit, its author and its date — a better audit trail than the supersedes chain
// it replaces, and one people already know how to read.
//
// `supersedes` survives for the case it is actually good at: a NEW fact that retires an older one
// while both stay worth reading (a design that changed, rather than a claim that was wrong).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// findFact locates one fact by id, or by a unique id prefix so a human does not have to type
// `20260918-031936-f24b87`.
func findFact(workspaceDir, id string) (fact, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return fact{}, fmt.Errorf("which fact? pass an id (see `ptln memory ls`)")
	}
	facts, err := readFacts(workspaceDir, true) // include superseded: editing one back to life is legitimate
	if err != nil {
		return fact{}, err
	}
	var hits []fact
	for _, f := range facts {
		if f.ID == id {
			return f, nil
		}
		if strings.HasPrefix(f.ID, id) {
			hits = append(hits, f)
		}
	}
	switch len(hits) {
	case 0:
		return fact{}, fmt.Errorf("no fact with id %q", id)
	case 1:
		return hits[0], nil
	default:
		var ids []string
		for _, f := range hits {
			ids = append(ids, f.ID)
		}
		return fact{}, fmt.Errorf("%q matches %d facts: %s", id, len(hits), strings.Join(ids, " "))
	}
}

// rewriteFact replaces a fact's body, keeping its id, kind, tags and creation time.
//
// The id is kept deliberately: it is cited in commit messages and in other facts, and minting a
// new one for an edit would break those while pretending the old claim still stands somewhere.
func rewriteFact(workspaceDir, id, body, by string) (fact, error) {
	f, err := findFact(workspaceDir, id)
	if err != nil {
		return fact{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fact{}, fmt.Errorf("a rewrite needs a new body — to remove a fact, delete its file")
	}
	if v := screenFact(f.Kind, body); v.Refuse {
		return fact{}, v.gateError()
	}
	f.Body = body
	f.Updated = time.Now().UTC()
	if by != "" {
		f.By = by
	}
	if _, err := writeFact(workspaceDir, f); err != nil {
		return fact{}, err
	}
	return f, nil
}

// appendToFact adds to a fact rather than replacing it — the live-session case, where a session
// learns more about something already recorded and the existing wording is still right.
func appendToFact(workspaceDir, id, add, by string) (fact, error) {
	f, err := findFact(workspaceDir, id)
	if err != nil {
		return fact{}, err
	}
	add = strings.TrimSpace(add)
	if add == "" {
		return fact{}, fmt.Errorf("nothing to append")
	}
	merged := f.Body + "\n\n" + add
	// The gate screens the ADDITION on its own, not the merged body: a long good fact would
	// otherwise carry a transient sentence in past the length and shape rules.
	if v := screenFact(f.Kind, add); v.Refuse {
		return fact{}, v.gateError()
	}
	f.Body = merged
	f.Updated = time.Now().UTC()
	if by != "" {
		f.By = by
	}
	if _, err := writeFact(workspaceDir, f); err != nil {
		return fact{}, err
	}
	return f, nil
}

// removeFact deletes a fact's file. Kept honest: this is a delete, and the only reason it is safe
// is that the file is in a git repo and the deletion shows up in `git status` before anyone commits
// it. Partyline does not commit your repo, so nothing is final until you say so.
func removeFact(workspaceDir, id string) (fact, error) {
	f, err := findFact(workspaceDir, id)
	if err != nil {
		return fact{}, err
	}
	p := f.Path
	if p == "" {
		p = filepath.Join(memoryDir(workspaceDir), f.ID+".md")
	}
	if err := os.Remove(p); err != nil {
		return fact{}, err
	}
	return f, nil
}
