package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// board_review_overlay.go — the review gate, in the terminal.
//
// `r` on a Review card used to open the PR in a browser. The product's central claim is "read what
// the agent wrote, keep what's right", and the reading happened on GitHub — or nowhere, for a run
// whose merge policy never opened a PR. This overlay puts the diff, the run's own summary and the
// accept action in the same screen the board lives on.
//
// THE DIFF IS COMPUTED LOCALLY, from the checkout this machine already has for the project. That is
// deliberate and not a shortcut: the daemon registry maps the label to a path the OWNER granted,
// the branch is fetched over the operator's own git credentials, and the control plane never learns
// a path or serves file contents. A machine without the checkout falls back to the PR in the
// browser — the exact behaviour `r` had before, now demoted to the fallback it should have been.

// reviewOverlay is the scrolling diff with the gate's actions at the bottom.
type reviewOverlay struct {
	card   api.BoardCard
	files  []diffFile
	doc    diffDoc
	stat   string
	branch string
	base   string
	scroll int
}

func (o *reviewOverlay) title() string {
	return "review — " + clipOneLine(o.card.Task, 46) + "  " + o.stat
}

func (o *reviewOverlay) footer() string {
	f := "↑↓ scroll · n/p file · a accept · o open PR · esc close"
	if o.card.PRURL == "" {
		f = "↑↓ scroll · n/p file · a accept · esc close"
	}
	return boardDim + f + reset
}

func (o *reviewOverlay) lines(m *boardModel, w, h int) []string {
	if o.scroll > max(0, len(o.doc.Lines)-h) {
		o.scroll = max(0, len(o.doc.Lines)-h)
	}
	if o.scroll < 0 {
		o.scroll = 0
	}
	end := min(len(o.doc.Lines), o.scroll+h-1)
	out := make([]string, 0, h)
	// Position line: which file the viewport is in, and how deep. A twelve-file diff with no
	// landmark is how a reviewer loses the thread between files.
	if len(o.doc.FilePaths) > 0 {
		i := o.doc.fileAt(o.scroll)
		out = append(out, boardDim+fmt.Sprintf("%s · file %d/%d · %s → %s",
			clipOneLine(o.doc.FilePaths[i], w-30), i+1, len(o.doc.FilePaths), o.base, o.branch)+reset)
	}
	out = append(out, o.doc.Lines[o.scroll:end]...)
	return out
}

func (o *reviewOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	switch {
	case len(b) == 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'A': // up
		o.scroll--
	case len(b) == 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'B': // down
		o.scroll++
	case len(b) == 3 && b[0] == 0x1b && b[1] == '[' && b[2] == '5': // page up (partial seq ok)
		o.scroll -= 20
	case len(b) == 3 && b[0] == 0x1b && b[1] == '[' && b[2] == '6': // page down
		o.scroll += 20
	case len(b) == 1 && (b[0] == 'j'):
		o.scroll++
	case len(b) == 1 && (b[0] == 'k'):
		o.scroll--
	case len(b) == 1 && b[0] == 'n':
		o.scroll = o.doc.nextFile(o.scroll)
	case len(b) == 1 && b[0] == 'p':
		o.scroll = o.doc.prevFile(o.scroll)
	case len(b) == 1 && b[0] == 'g':
		o.scroll = 0
	case len(b) == 1 && b[0] == 'G':
		o.scroll = len(o.doc.Lines)
	case len(b) == 1 && b[0] == 'o':
		if o.card.PRURL != "" {
			m.openURL(o.card.PRURL, "PR")
		}
	case len(b) == 1 && b[0] == 'a':
		// Accept from inside the reading. Same action, same guard, same wire as the board's own
		// accept — the overlay adds a reading surface, never a second decision path.
		for _, act := range boardActions(o.card) {
			if act.Key == "accept" {
				return true, m.runAction(c, o.card, act)
			}
		}
		m.setToast("this run has no accept action in its current state", false)
	case len(b) == 1 && (b[0] == 0x1b || b[0] == 'q'):
		return true, false
	}
	return false, false
}

// openReviewOverlay resolves the run's branch, computes the diff locally, and opens the overlay.
//
// Falls back to the old behaviour — the PR in a browser — the moment anything is missing: no local
// checkout, no branch recorded, git failing. Review must never be BLOCKED by this feature; on any
// gap the reviewer lands where they always did.
func (m *boardModel) openReviewOverlay(c *api.Client, card api.BoardCard) {
	snap, err := c.GetRun(card.ID)
	if err != nil {
		m.reviewFallback(card, "could not read the run: "+err.Error())
		return
	}
	branch := lastTaskBranch(snap)
	if branch == "" {
		m.reviewFallback(card, "")
		return
	}
	reg := loadDaemonRegistry()
	p := projectByLabel(reg, snap.Run.ProjectLabel)
	if p == nil {
		m.reviewFallback(card, "this machine has no checkout of "+snap.Run.ProjectLabel)
		return
	}

	// Fetch is best-effort with a short leash: the branch is usually already here (this machine
	// often built it), and a review gate that hangs on a slow remote is worse than one that reads
	// a commit behind.
	fetchBranch(p.Path, branch)

	base := detectBaseBranch(p.Path)
	text, err := reviewGitDiff(p.Path, base, branch)
	if err != nil {
		m.reviewFallback(card, "git diff failed: "+err.Error())
		return
	}
	files := parseUnifiedDiff(text)
	o := &reviewOverlay{card: card, files: files, doc: buildDiffDoc(files), stat: diffStatLine(files), branch: branch, base: base}
	m.openOverlay(o)
}

// reviewFallback is the pre-overlay behaviour, with the reason the overlay could not open said out
// loud — a silent downgrade to the browser would read as the feature not existing.
func (m *boardModel) reviewFallback(card api.BoardCard, why string) {
	if why != "" {
		m.setToast(why+" — opening the PR instead", false)
	}
	if card.PRURL != "" {
		m.openURL(card.PRURL, "PR")
		return
	}
	if card.NoPR != nil {
		m.openOverlay(&noticeOverlay{heading: "no PR to review", body: wrapPlain(
			noPRExplanation(card.NoPR.Kind, card.NoPR.Detail), 70)})
		return
	}
	m.setToast("this run has no PR yet", false)
}

// lastTaskBranch is the branch the reviewer signs off on: the LAST task's, because a chain builds
// forward and the final branch contains the run's whole result.
func lastTaskBranch(snap *api.RunSnapshot) string {
	for i := len(snap.Tasks) - 1; i >= 0; i-- {
		if b := strings.TrimSpace(snap.Tasks[i].Branch); b != "" {
			return b
		}
	}
	return ""
}

// ── git plumbing ─────────────────────────────────────────────────────────────────────────────────

// gitReviewTimeout bounds each git call. The reviewer is sitting at a modal; five seconds of
// spinner is an annoyance, thirty is a bug report.
const gitReviewTimeout = 5 * time.Second

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
	case <-time.After(gitReviewTimeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("git %s timed out", args[0])
	}
	return string(out), err
}

func fetchBranch(dir, branch string) {
	_, _ = gitIn(dir, "fetch", "--quiet", "origin", branch)
}

// detectBaseBranch answers "diff against what?" from the checkout itself: origin's HEAD when the
// remote declares one, then main, then master. Wrong-base is recoverable (the diff is merely
// bigger); no-base is not, so every path returns something.
func detectBaseBranch(dir string) string {
	if out, err := gitIn(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			return strings.TrimPrefix(s, "origin/")
		}
	}
	for _, cand := range []string{"main", "master"} {
		if _, err := gitIn(dir, "rev-parse", "--verify", "--quiet", "origin/"+cand); err == nil {
			return cand
		}
	}
	return "main"
}

// reviewGitDiff is the PR-style diff: three dots, so the comparison is against the merge base and the
// reviewer sees what THIS BRANCH ADDS — not every commit that landed on the base since it forked,
// which a two-dot diff would mix in and attribute to the agent.
func reviewGitDiff(dir, base, branch string) (string, error) {
	for _, ref := range []string{"origin/" + branch, branch} {
		out, err := gitIn(dir, "diff", "--no-color", "origin/"+base+"..."+ref)
		if err == nil {
			return out, nil
		}
	}
	return "", fmt.Errorf("branch %q not found locally or on origin", branch)
}

// clipOneLine is a plain-text clip for titles (the diff body is clipped by the overlay's renderer).
func clipOneLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
