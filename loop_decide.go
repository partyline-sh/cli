package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Loops decide for themselves (the settings' default): nothing waits for the person. Whenever a
// run would start waiting (a brief or batch to approve, a disputed finding, a batch's deferred
// findings), partyline starts `ptln loop _decide <id>`, which makes the decision through the same
// functions the approval popup uses:
//
//   - a brief or batch waiting for approval is approved: the reviewers have already read it, a
//     batch with open P1s was sent back to its session to revise before it got here, and the
//     reviewers go on reviewing every round;
//   - a disputed finding goes to a model that did not build the work (the arbiter, else the first
//     reviewer), reading it read-only with the builder's reason and, when a Jev key is saved, Jev's
//     estimate of whether the finding stands. Its ruling stands whatever its confidence; with no
//     valid answer the finding stands, so the work is fixed rather than a P1 waved through.
//
// The watcher's tick starts a decider for any waiting run that has none (decideWaiting), so a
// decision missed by a crash is made later rather than never.

// loopsDecide: decisions are the loops' own, not the person's.
func loopsDecide() bool {
	s, _ := loadSettings()
	return !s.AskMe
}

// spawnDecide starts the decider for run id, detached; a variable so tests run it in-process.
var spawnDecide = func(id string) {
	_ = os.WriteFile(loopFile(id, "decide.started"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
	log, err := os.OpenFile(loopFile(id, "decide.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	cmd := exec.Command(selfExe(), "loop", "_decide", id)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// decideWhenWaiting is called on every state change: a run that starts waiting gets its decider.
func decideWhenWaiting(r *loopRun) {
	if r.Remote == nil && loopWaiting(r) && loopsDecide() {
		spawnDecide(r.ID)
	}
}

// decideWaiting, on the watcher's tick, starts a decider for a waiting run whose last one is more
// than five minutes old (it crashed, or the machine slept).
func decideWaiting() {
	if !loopsDecide() {
		return
	}
	for _, r := range loopWaitingRuns() {
		if b, err := os.ReadFile(loopFile(r.ID, "decide.started")); err == nil {
			if at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil && time.Since(at) < 5*time.Minute {
				continue
			}
		}
		spawnDecide(r.ID)
	}
}

// decidedBy names who decided, in the messages the session reads: the person in the popup, or the
// loop itself in a decider.
var decidedBy = "The user"

// loopDecideMain is the hidden `ptln loop _decide <id>`.
func loopDecideMain(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: ptln loop _decide <id>"))
	}
	r := mustLoop(args)
	lock, err := os.OpenFile(loopFile(r.ID, "decide.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		fatal(err)
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return // another decider has it
	}
	decidedBy = "The loop (decisions are the loop's own)"
	if line, err := loopDecide(r.ID); err != nil {
		fmt.Println("not decided:", err)
	} else if line != "" {
		fmt.Println(line)
	}
}

// loopDecide makes the decision run id is waiting for, if any, and says what it decided.
func loopDecide(id string) (string, error) {
	r, err := loadLoopRun(id)
	if err != nil || !loopsDecide() {
		return "", err
	}
	switch r.State {
	case loopAwaiting:
		if err := loopApproveRun(r.ID, r.Revision); err != nil {
			return "", err
		}
		return fmt.Sprintf("approved brief %d", r.Revision), nil
	case loopAwaitYou:
		var t loopTracked
		readLoopJSON(r.ID, "dispute.json", &t)
		if t.ID == "" {
			return "", fmt.Errorf("waiting, but no disputed finding is recorded")
		}
		ruling := decideRuling(r, t)
		_ = r.save(loopEvent{Type: "loop_ruling", Note: ruling.Reason, Data: map[string]any{"finding": t.ID, "decision": ruling.Decision,
			"confidence": ruling.Confidence, "jev": ruling.Jev, "engine": ruling.Engine}})
		if err := loopDecideDispute(r.ID, t.ID, ruling.Decision); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: %s (%s)", t.ID, ruling.Decision, ruling.Reason), nil
	}
	return "", nil
}

type loopRuling struct {
	arbiterRuling
	Jev    string // Jev's estimate as given to the model, "" when there was none
	Engine string
}

// decideRuling has a model that did not build the work rule on the disputed finding t.
func decideRuling(r *loopRun, t loopTracked) loopRuling {
	out := loopRuling{arbiterRuling: arbiterRuling{Decision: "fix"}}
	engine := ""
	if r.Config != nil && r.Config.Arbiter != "" {
		engine = r.Config.Arbiter
	} else if r.Config != nil && len(r.Config.Reviewers) > 0 && len(r.Config.Reviewers[0]) > 0 {
		engine = r.Config.Reviewers[0][0]
	} else if es, err := parseLoopEngines(r.Review); err == nil {
		engine = es[0].String()
	}
	out.Engine = engine
	ctx, cancel := context.WithTimeout(context.Background(), loopReviewTimeout)
	defer cancel()
	jevLine := ""
	if p, ok := jevFindingStands(ctx, latestBrief(r), t); ok {
		out.Jev = fmt.Sprintf("%.2f", p)
		jevLine = fmt.Sprintf("An outside quick-judgment model (Jev) estimates the finding is right with probability %.2f. It did not read the code; weigh it, and decide from the code.\n\n", p)
	}
	es, err := parseLoopEngines(engine)
	if err != nil || len(es) == 0 {
		out.Reason = "no model to rule, so the finding stands"
		return out
	}
	tree, err := workTreeID(r.Repo)
	if err != nil {
		out.Reason = "could not read the work, so the finding stands: " + err.Error()
		return out
	}
	dir, cleanup, err := treeSnapshot(r.Repo, tree)
	if err != nil {
		out.Reason = "could not read the work, so the finding stands: " + err.Error()
		return out
	}
	defer cleanup()
	prompt := strings.Replace(arbiterPrompt(latestBrief(r), t), "Give your real confidence: below 0.8 the person decides instead of you.\n",
		"Give your real confidence. Your ruling stands either way: nobody else is asked.\n", 1)
	prompt = strings.Replace(prompt, "The author's dispute:", jevLine+"The author's dispute:", 1)
	text, tok, err := runReviewer(ctx, es[0], dir, prompt)
	recordLoopUsage(r.ID, loopUsage{Role: "decider", Round: r.Round, Engine: es[0].String(), Tokens: tok})
	if err != nil {
		out.Reason = "the model did not answer, so the finding stands: " + err.Error()
		return out
	}
	ruling := parseArbiterRuling(text)
	if ruling.Err != "" {
		out.Reason = "no valid ruling, so the finding stands: " + ruling.Err
		return out
	}
	out.arbiterRuling = ruling
	return out
}
