package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Reviewers are contractors: each reviewer of a run's round runs in a contractor window (role
// reviewer) under the run's session, so a review can be watched while it reads. The command, its
// read-only posture, environment and limits are runReviewer's, unchanged; only where it runs moves.

// reviewAt says where a review runs: for a run (owner) as its contractor, labelled with the round
// and slot; with no owner (a standalone `ptln loop review`), in this process.
type reviewAt struct {
	owner *loopRun
	label string // "r2-1": round 2, slot 1
}

// reviewResult is what the reviewer contractor hands back: the reviewer's final message and token
// use, or its failure with the engine's own diagnostics (for limits).
type reviewResult struct {
	Text   string     `json:"text"`
	Tokens loopTokens `json:"tokens"`
	Err    string     `json:"error,omitempty"`
	Diag   string     `json:"diag,omitempty"`
}

func (at reviewAt) run(ctx context.Context, e loopEngine, dir, prompt string) (string, loopTokens, error) {
	if at.owner == nil {
		return runReviewer(ctx, e, dir, prompt)
	}
	return runReviewerContractor(ctx, at, e, dir, prompt)
}

// reviewerGrace is how long a reviewer contractor may still take after the round's own timeout.
var reviewerGrace = time.Minute

// runReviewerContractor starts the reviewer as a contractor running `ptln loop _review` and waits
// for its result, for as long as the review may take. A variable so tests can see it.
var runReviewerContractor = func(ctx context.Context, at reviewAt, e loopEngine, dir, prompt string) (string, loopTokens, error) {
	r := at.owner
	promptFile := loopFile(r.ID, "review-"+at.label+"-"+safeFileName(e.String())+".prompt.md")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return "", loopTokens{}, err
	}
	c := &contractor{ID: newContractorID(), Parent: builderParent(r), Loop: r.ID, Round: r.Round, Role: "reviewer", Engine: e.String(),
		Title: "review " + at.label, Dir: dir}
	c.Argv = []string{selfExe(), "loop", "_review", r.ID, promptFile, e.String(), dir, c.ID}
	if err := startContractor(c); err != nil {
		return "", loopTokens{}, fmt.Errorf("could not start the reviewer: %v", err)
	}
	// The contractor times its own review out (loopReviewTimeout) and ends its engine's process
	// group; this waits a minute longer, and only then closes the window, whose hangup the reviewer
	// also answers by ending the engine.
	var grace time.Time // set when the round gives up: a minute from then
	for {
		if got, err := loadContractor(c.ID); err == nil && got.State != "running" && got.State != "starting" {
			break
		}
		if ctx.Err() != nil {
			if grace.IsZero() {
				grace = time.Now().Add(reviewerGrace)
			}
			if time.Now().After(grace) {
				stopContractor(c.ID)
				return "", loopTokens{}, fmt.Errorf("timed out")
			}
		}
		time.Sleep(time.Second)
	}
	var res reviewResult
	readLoopJSON(r.ID, "review-"+c.ID+".result.json", &res)
	switch {
	case res.Err != "":
		return res.Text, res.Tokens, &reviewerFailure{res.Err, res.Diag}
	case res.Text == "":
		return "", res.Tokens, fmt.Errorf("the reviewer left no review")
	}
	return res.Text, res.Tokens, nil
}

// loopReviewMain is the hidden `ptln loop _review <id> <prompt-file> <engine> <dir> <contractor>`:
// a reviewer contractor's command. It runs the review as a round always has and writes the result
// for the round to read.
func loopReviewMain(args []string) {
	if len(args) != 5 {
		fatal(fmt.Errorf("usage: ptln loop _review <id> <prompt-file> <engine> <dir> <contractor>"))
	}
	if res := runReviewContractor(args); res.Err != "" {
		os.Exit(1) // the chip reads ✗; the round reads the result file either way
	}
}

// runReviewContractor runs the review and writes its result before returning. A hangup or SIGTERM (the
// window closed, the round gave up) cancels it, which ends the engine's process group.
func runReviewContractor(args []string) reviewResult {
	res := reviewResult{}
	r, err := loadLoopRun(args[0])
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer func() { writeLoopJSON(r.ID, "review-"+args[4]+".result.json", res) }()
	prompt, err := os.ReadFile(args[1])
	if err != nil {
		res.Err = err.Error()
		return res
	}
	es, err := parseLoopEngines(args[2])
	if err != nil {
		res.Err = err.Error()
		return res
	}
	fmt.Printf("partyline reviewer %s for loop %s, reading the work read-only\n", es[0], r.ID)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, loopReviewTimeout)
	defer cancel()
	res.Text, res.Tokens, err = runReviewer(ctx, es[0], args[3], string(prompt))
	if err != nil {
		res.Err = err.Error()
		var failed *reviewerFailure
		if errors.As(err, &failed) {
			res.Diag = failed.diag
		}
		fmt.Println("the review failed:", err)
		return res
	}
	fmt.Println(termSafeLines(res.Text))
	return res
}
