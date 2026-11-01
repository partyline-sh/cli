package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"partyline.sh/partyline/internal/remote"
)

// These commands are reachable only through remoteInputStore.handle on an
// authenticated phone channel. There is deliberately no CLI or HTTP entry point.
type remoteLoopChallenge struct {
	Phone, Loop, View, Action, Code, Fingerprint string
	Revision                                     int
	Until                                        time.Time
}

type remoteLoopCommand struct {
	Action   string `json:"action"`
	Token    string `json:"token"`
	Code     string `json:"code"`
	Revision int    `json:"revision"`
	Note     string `json:"note"`
}

// control holds the input store and phone authorization locks for its lifetime.
func (s *remoteInputStore) control(phone string, m remote.Message, now time.Time) (string, string, any) {
	var data any
	var err error
	var reverseBatch, reverseFinding string
	if m.Type == "shell_open" {
		if m.Data != nil || m.Pane != "" {
			return "refused", "Shell starts in your home directory; no command or target accepted", nil
		}
		if err = remoteControlAudit(phone, "shell_open", "", "", "", now); err == nil {
			var pane string
			pane, err = openRemoteShell()
			data = map[string]string{"pane": pane}
			if err != nil {
				err = fmt.Errorf("shell creation failed; delivery may be partial: %w", err)
			}
		}
	} else {
		target, plan, resolveErr := remoteLoopTarget(m.Pane)
		if resolveErr != nil {
			return "refused", resolveErr.Error(), nil
		}
		err = withRunLock(target, func(r *loopRun) error {
			if m.Type == "loop_detail" {
				data = remoteLoopView(r, plan)
				return remoteDetailSize(data)
			}
			b, e := json.Marshal(m.Data)
			var cmd remoteLoopCommand
			if e != nil || len(b) > 16*1024 || json.Unmarshal(b, &cmd) != nil || !utf8.ValidString(cmd.Note) {
				return errors.New("invalid loop command")
			}
			if !remoteLoopActionAllowed(r, cmd.Action) || plan > 0 && cmd.Action != "stop" && cmd.Action != "delete" {
				return errors.New("that action is not available in the loop's current state")
			}
			fingerprint, e := remoteLoopFingerprint(r)
			if e != nil {
				return e
			}
			if m.Type == "loop_prepare" {
				if until := gateLockedFor(r.ID); until > 0 {
					return fmt.Errorf("too many wrong codes; try again in %s", until)
				}
				if s.challenges == nil {
					s.challenges = map[string]remoteLoopChallenge{}
				}
				for token, ch := range s.challenges {
					if !now.Before(ch.Until) || ch.Phone == phone && ch.Loop == r.ID {
						delete(s.challenges, token)
					}
				}
				if len(s.challenges) >= 256 {
					return errors.New("too many pending confirmations")
				}
				var random [32]byte
				if _, e := rand.Read(random[:]); e != nil {
					return e
				}
				token := hex.EncodeToString(random[:])
				n, e := rand.Int(rand.Reader, big.NewInt(10000))
				if e != nil {
					return e
				}
				code := fmt.Sprintf("%04d", n.Int64())
				detail := remoteLoopView(r, plan)
				if err := remoteDetailSize(detail); err != nil {
					return err
				}
				s.challenges[token] = remoteLoopChallenge{Phone: phone, Loop: r.ID, View: m.Pane, Action: cmd.Action, Code: code, Fingerprint: fingerprint, Revision: r.Revision, Until: now.Add(5 * time.Minute)}
				data = map[string]any{"token": token, "code": code, "revision": r.Revision, "action": cmd.Action, "detail": detail}
				return nil
			}
			ch, ok := s.challenges[cmd.Token]
			if !ok || ch.Phone != phone || ch.Loop != r.ID || ch.View != m.Pane || ch.Action != cmd.Action || !now.Before(ch.Until) {
				return errors.New("no live confirmation for this action; read the loop and confirm again")
			}
			if ch.Revision != r.Revision || cmd.Revision != r.Revision || ch.Fingerprint != fingerprint {
				delete(s.challenges, cmd.Token)
				return errors.New("the loop changed since you read it; nothing was decided")
			}
			if until := gateLockedFor(r.ID); until > 0 {
				return fmt.Errorf("too many wrong codes; try again in %s", until)
			}
			if subtle.ConstantTimeCompare([]byte(ch.Code), []byte(cmd.Code)) != 1 {
				gateMiss(r.ID)
				return errors.New("the code did not match; nothing was decided")
			}
			if cmd.Action == "reject" && strings.TrimSpace(cmd.Note) == "" {
				return errors.New("enter a note explaining the rejection")
			}
			// Audit intent before effects. Consume the challenge even on partial failure;
			// retries with the same input ID receive the original cached result.
			if e := remoteControlAudit(phone, "loop_action", r.ID, cmd.Token, cmd.Action, now); e != nil {
				return e
			}
			delete(s.challenges, cmd.Token)
			by := func() map[string]any {
				return map[string]any{"confirmed_by": "paired_phone_code", "phoneId": phone, "gate": cmd.Token, "revision": r.Revision}
			}
			switch cmd.Action {
			case "approve":
				e = loopApproveRunLocked(r, r.Revision, by)
			case "reject":
				e = loopRejectRunLocked(r, r.Revision, cmd.Note, by)
			case "fix", "dismiss":
				var finding loopTracked
				readLoopJSON(r.ID, "dispute.json", &finding)
				e = loopDecideDisputeLocked(r, finding.ID, cmd.Action, by)
			case "reverse":
				standing := gateStanding(r)
				reverseBatch, reverseFinding = r.Batch, fmt.Sprintf("S%d-%s", standing[0].Step, standing[0].Finding.ID)
				return nil
			case "stop", "delete", "resume":
				// These existing commands acquire their own run/batch locks below.
				return nil
			}
			if e != nil {
				return fmt.Errorf("action failed; delivery may be partial: %w", e)
			}
			return nil
		})
		// Stop follows the current batch step; delete cancels the whole batch. Never
		// call either while holding a run lock. A validated, consumed challenge is
		// required (represented by the audited intent) before getting here.
		if err == nil && m.Type == "loop_action" {
			b, _ := json.Marshal(m.Data)
			var cmd remoteLoopCommand
			_ = json.Unmarshal(b, &cmd)
			switch cmd.Action {
			case "reverse":
				_, err = batchReverse(reverseBatch, reverseFinding)
			case "stop":
				err = loopStopRun(target)
			case "delete":
				err = loopDelete(target)
			case "resume":
				err = loopResumeRun(target, "resumed by paired phone")
			}
			if err != nil {
				err = fmt.Errorf("action failed; delivery may be partial: %w", err)
			}
		}
	}
	if err != nil {
		return "refused", err.Error(), nil
	}
	return "ok", "", data
}

// Planned cards identify the approved brief, not a nonexistent run. Their only
// effects stop/delete the whole batch, using its current run and fingerprint.
func remoteLoopTarget(id string) (string, int, error) {
	if loopIDPattern.MatchString(id) {
		return id, 0, nil
	}
	parts := strings.Split(id, "-")
	if len(parts) != 3 || parts[0] != "plan" || !loopIDPattern.MatchString(parts[1]) {
		return "", 0, errors.New("invalid loop id")
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, errors.New("invalid planned step")
	}
	b, err := loadBatch(parts[1])
	if err != nil || n <= len(b.Runs) || n > len(b.Briefs) || len(b.Runs) == 0 {
		return "", 0, errors.New("planned step is no longer available; refresh the board")
	}
	return b.Runs[len(b.Runs)-1], n, nil
}

func remoteLoopView(r *loopRun, plan int) map[string]any {
	d := remoteLoopDetail(r)
	if plan == 0 {
		return d
	}
	b, err := loadBatch(r.Batch)
	if err != nil || plan <= len(b.Runs) || plan > len(b.Briefs) {
		return map[string]any{"id": r.ID, "title": "Step started; refresh the board", "state": r.State, "actions": []string{}, "dispute": map[string]any{}}
	}
	d["id"], d["title"], d["state"], d["brief"] = fmt.Sprintf("plan-%s-%d", b.ID, plan), briefTitle(b.Briefs[plan-1]), "planned", b.Briefs[plan-1]
	actions := []string{"delete"}
	if r.live() {
		actions = append([]string{"stop"}, actions...)
	}
	d["actions"], d["history"] = actions, fmt.Sprintf("Planned step %d of %d; not started. Controls affect the entire batch.\n\n%s", plan, len(b.Briefs), batchSummary(b))
	return d
}

// Refuse oversized details explicitly rather than truncating the brief a person
// is asked to approve or stalling the encrypted snapshot queue.
func remoteDetailSize(data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if len(b) > 512*1024 {
		return errors.New("loop details exceed 512 KiB; read this loop on the computer")
	}
	return nil
}

func remoteLoopActionAllowed(r *loopRun, action string) bool {
	switch action {
	case "approve", "reject":
		return r.State == loopAwaiting
	case "fix", "dismiss":
		return r.State == loopAwaitYou
	case "stop":
		return r.live()
	case "resume":
		return r.State == loopPaused
	case "reverse":
		return len(gateStanding(r)) > 0
	case "delete":
		return true
	}
	return false
}

func remoteLoopFingerprint(r *loopRun) (string, error) {
	var dispute loopTracked
	readLoopJSON(r.ID, "dispute.json", &dispute)
	var batch any
	if r.Batch != "" {
		b, err := loadBatch(r.Batch)
		if err != nil {
			return "", err
		}
		batch = b
	}
	b, err := json.Marshal([]any{r, latestBrief(r), dispute, batch})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func remoteLoopDetail(r *loopRun) map[string]any {
	row := tabRow{RunID: r.ID, Repo: r.Repo, Goal: briefGoal(latestBrief(r))}
	actions := []string{}
	for _, a := range []string{"approve", "reject", "fix", "dismiss", "resume", "reverse", "stop", "delete"} {
		if remoteLoopActionAllowed(r, a) {
			actions = append(actions, a)
		}
	}
	target, _ := loopDeleteTarget(r.ID)
	var dispute loopTracked
	readLoopJSON(r.ID, "dispute.json", &dispute)
	return map[string]any{"id": r.ID, "title": tabTitle(r), "state": r.State, "revision": r.Revision, "brief": latestBrief(r), "history": remoteLoopHistory(r, row), "dispute": dispute, "actions": actions, "deleteTarget": target, "pr": r.PR}
}

func remoteLoopHistory(r *loopRun, row tabRow) string {
	var out strings.Builder
	out.WriteString(tabDetailText(row))
	out.WriteString("\n\nConfigured checks:\n" + strings.Join(r.Checks, "\n"))
	out.WriteString("\n\nReviewers:\n" + r.Review)
	if r.Config != nil {
		out.WriteString("\n" + configDescription(r.Config))
		if r.Config.Quality != "" {
			out.WriteString("\n\nApproved quality standard:\n" + r.Config.Quality)
		}
	}
	var baseline []loopCheck
	readLoopJSON(r.ID, "baseline.json", &baseline)
	if len(baseline) > 0 {
		out.WriteString("\n\nBaseline checks:\n" + remoteChecksText(baseline))
	}
	for n := 1; n <= r.Round; n++ {
		var checks loopCheckRound
		readLoopJSON(r.ID, fmt.Sprintf("checks-%d.json", n), &checks)
		fmt.Fprintf(&out, "\n\nRound %d check output:\n%s", n, remoteChecksText(checks.Checks))
		var findings []loopTracked
		readLoopJSON(r.ID, fmt.Sprintf("findings-%d.json", n), &findings)
		if len(findings) > 0 {
			out.WriteString("\n\nAll findings:\n")
			for _, f := range findings {
				out.WriteString(formatTracked([]loopTracked{f}) + "\n")
				if f.Dropped != "" {
					out.WriteString("Dropped: " + f.Dropped + "\n")
				}
				if f.Dispute != "" {
					out.WriteString("Dispute: " + f.Dispute + "\n")
				}
				if f.Decision != "" {
					out.WriteString("Decision: " + f.Decision + "\n")
				}
			}
		}
		report, err := os.ReadFile(loopFile(r.ID, fmt.Sprintf("ready-%d.md", n)))
		if err == nil {
			out.WriteString("\n\nBuilder report:\n" + string(report))
		}
	}
	for _, dismissal := range gateStanding(r) {
		fmt.Fprintf(&out, "\n\nArbiter dismissal S%d-%s (can be reversed):\n%s\nReason: %s\nConfidence: %.2f", dismissal.Step, dismissal.Finding.ID, formatTracked([]loopTracked{dismissal.Finding}), dismissal.Reason, dismissal.Confidence)
	}
	return out.String()
}

func remoteChecksText(checks []loopCheck) string {
	var out strings.Builder
	for _, check := range checks {
		fmt.Fprintf(&out, "$ %s (exit %d)\n%s\n", check.Cmd, check.Exit, check.Tail)
	}
	return out.String()
}

func remoteControlAudit(phone, action, loop, gate, choice string, now time.Time) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(map[string]any{"at": now.UTC(), "action": action, "phoneId": phone, "loop": loop, "gate": gate, "choice": choice}); err != nil {
		return err
	}
	return f.Sync()
}

func openRemoteShell() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	args := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=" + tmuxSessionName + ":", "-n", "Terminal", "-c", home}
	if tmuxCmd("has-session", "-t", "="+tmuxSessionName).Run() != nil {
		args = []string{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "-n", "Terminal", "-c", home}
	}
	out, err := tmuxCmd(args...).Output()
	return strings.TrimSpace(string(out)), err
}
