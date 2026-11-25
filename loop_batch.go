package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// A batch is an ordered set of briefs the person approves once, after the reviewers have read them
// as a set (loop_batch_review.go). Each step is an ordinary run; the first carries the batch as its
// brief until the person approves it, and each later one starts already approved, from the brief
// whose hash the person approved. Steps run one after another on one branch, so nothing stacks.

type loopBatch struct {
	Config     *loopConfig       `json:"config,omitempty"`
	ConfigHash string            `json:"config_hash,omitempty"`
	ID         string            `json:"id"`
	Briefs     []string          `json:"briefs"`           // step briefs, in order
	Hashes     []string          `json:"hashes,omitempty"` // sha256 of each brief, recorded at approval
	Runs       []string          `json:"runs"`             // the run of each step started so far
	Disputed   []loopTracked     `json:"disputed,omitempty"`
	Deferred   []loopTracked     `json:"deferred,omitempty"`   // P1s disputed during a step, for the person
	Arbitrated []loopArbitration `json:"arbitrated,omitempty"` // the arbiter's rulings (loop_arbiter.go)
	Ends       []string          `json:"ends,omitempty"`       // the commit each step ended at
	Stopped    bool              `json:"stopped,omitempty"`    // the person stopped the batch
	Failed     bool              `json:"failed,omitempty"`     // a step failed, so the batch ended
	Branch     string            `json:"branch,omitempty"`     // the branch the person approved it on
	Engine     string            `json:"engine,omitempty"`     // the engine that started it: the default builder
	Deferral   string            `json:"deferral,omitempty"`   // the run that puts deferred findings to the person
	Created    time.Time         `json:"created"`
}

func batchDir(id string) string { return filepath.Join(loopsDir(), "batch-"+id) }

func loadBatch(id string) (*loopBatch, error) {
	if !loopIDPattern.MatchString(id) {
		return nil, fmt.Errorf("no batch %q", id)
	}
	var b loopBatch
	data, err := os.ReadFile(filepath.Join(batchDir(id), "batch.json"))
	if err != nil {
		return nil, fmt.Errorf("no batch %q", id)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("batch %s: unreadable: %v", id, err)
	}
	if b.ID != id {
		return nil, fmt.Errorf("batch %s: its file names batch %q", id, b.ID)
	}
	return &b, nil
}

func (b *loopBatch) save() error {
	if err := os.MkdirAll(batchDir(b.ID), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	tmp := filepath.Join(batchDir(b.ID), "batch.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(batchDir(b.ID), "batch.json"))
}

// withBatch runs fn on the batch as it is on disk, holding its lock. Taken inside a run's lock,
// never around one.
func withBatch(id string, fn func(b *loopBatch) error) error {
	if !loopIDPattern.MatchString(id) {
		return fmt.Errorf("no batch %q", id)
	}
	f, err := os.OpenFile(filepath.Join(batchDir(id), "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("no batch %q", id)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	b, err := loadBatch(id)
	if err != nil {
		return err
	}
	if err := fn(b); err != nil {
		return err
	}
	return b.save()
}

// batchContinues: r is the batch's latest step and another step, or the review of deferred
// findings, is still to come.
func batchContinues(r *loopRun) bool {
	if r.Batch == "" {
		return false
	}
	b, err := loadBatch(r.Batch)
	if err != nil || b.Stopped || b.Failed || b.Deferral != "" || len(b.Runs) == 0 || b.Runs[len(b.Runs)-1] != r.ID {
		return false
	}
	return r.Step < len(b.Briefs) || (r.Step == len(b.Briefs) && anyDeferred(b))
}

// batchUnclosed: r is the batch's last step and `batch next` has not yet recorded where it ended.
func batchUnclosed(r *loopRun) bool {
	if r.Batch == "" {
		return false
	}
	b, err := loadBatch(r.Batch)
	return err == nil && !b.Stopped && !b.Failed && r.Step == len(b.Briefs) && len(b.Ends) < len(b.Briefs)
}

// batchPending: r is the first step of a batch the person has not approved yet, so its brief is the
// whole batch.
func batchPending(r *loopRun) bool {
	if r.Batch == "" || r.Step != 1 {
		return false
	}
	b, err := loadBatch(r.Batch)
	return err == nil && len(b.Hashes) == 0
}

// approvedBriefFile holds the last brief text the person approved for a batch step: the step's
// brief from the batch, or a revision they approved in the popup. Scope reviews compare against it,
// never against an unattended revision, so a chain of small revisions cannot walk away from it.
const approvedBriefFile = "approved-brief.md"

// maxFreeRevisions is how many revisions the scope review may accept in one step without the person.
const maxFreeRevisions = 3

func briefHash(brief string) string {
	sum := sha256.Sum256([]byte(brief))
	return hex.EncodeToString(sum[:])
}

func briefTitle(brief string) string {
	for _, l := range strings.Split(brief, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	return "(untitled)"
}

// batchDocument is what the person reads and approves: a Goal section listing every step, the
// review's outcome and any disputed findings, then each brief in full.
func batchDocument(b *loopBatch, review string) string {
	var d strings.Builder
	fmt.Fprintf(&d, "# Batch of %d steps\n\n## Goal\n", len(b.Briefs))
	for i, br := range b.Briefs {
		fmt.Fprintf(&d, "%d. %s: %s\n", i+1, briefTitle(br), briefGoal(br))
	}
	if b.Config != nil {
		fmt.Fprintf(&d, "\n## Model config\n%s\n", configDescription(b.Config))
	}
	if review != "" {
		fmt.Fprintf(&d, "\n## Batch review\n%s\n", review)
	}
	if len(b.Disputed) > 0 {
		fmt.Fprintf(&d, "\n## Disputed by the session\nApproving the batch dismisses these.\n")
		for _, t := range b.Disputed {
			fmt.Fprintf(&d, "\n%s\n  the session says: %s\n", formatTracked([]loopTracked{t}), termSafe(t.Dispute))
		}
	}
	for i, br := range b.Briefs {
		fmt.Fprintf(&d, "\n## Step %d\n%s\n", i+1, demoteHeadings(br))
	}
	return d.String()
}

// demoteHeadings moves a brief's headings down three levels, past the three levels mdSections
// reads, so nothing in a step (a heading, a fenced "# " line) can stand in for the batch
// document's own Goal or Batch review sections.
func demoteHeadings(brief string) string {
	lines := strings.Split(strings.TrimSpace(brief), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "#") {
			lines[i] = "###" + l
		}
	}
	return strings.Join(lines, "\n")
}

// batchApproved records the person's approval of every brief in the batch. Called under the first
// step's run lock, as part of approving that run.
func batchApproved(r *loopRun) (map[string]any, error) {
	var data map[string]any
	err := withBatch(r.Batch, func(b *loopBatch) error {
		if err := r.checkConfig(); err != nil {
			return err
		}
		// Both ways: the document the person approved has a config section exactly when the batch
		// has a config, and it is this one. Only the document's own top-level sections count: the
		// briefs inside it are demoted below the level mdSections reads.
		shown, has := mdSections(latestBrief(r))["model config"]
		if b.Config != nil && (!has || shown != configDescription(b.Config)) || b.Config == nil && has {
			return fmt.Errorf("batch config changed since review")
		}
		b.ConfigHash = configHash(b.Config)
		b.Hashes = make([]string, len(b.Briefs))
		for i, br := range b.Briefs {
			b.Hashes[i] = briefHash(br)
		}
		// From here on the run builds step 1, so its brief is step 1's.
		r.Revision++
		r.Free++
		if err := os.WriteFile(loopFile(r.ID, fmt.Sprintf("brief-%d.md", r.Revision)), []byte(b.Briefs[0]), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(loopFile(r.ID, approvedBriefFile), []byte(b.Briefs[0]), 0o600); err != nil {
			return err
		}
		// Every step builds on the branch the person approved the batch on.
		branch, err := gitMem(r.Repo, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil || branch == "HEAD" {
			return fmt.Errorf("a batch runs on a branch; this worktree has none checked out")
		}
		b.Branch = branch
		data = map[string]any{"batch": b.ID, "steps": len(b.Briefs), "hashes": b.Hashes, "branch": branch}
		return nil
	})
	return data, err
}
