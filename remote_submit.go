package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"partyline.sh/partyline/internal/remote"
)

type remoteSubmission struct {
	pane, identity, fingerprint string
	until                       time.Time
	used                        bool
}

var remoteSubmitWait = time.Sleep
var remoteSubmitSnapshot = func(pane string) (string, error) {
	b, e := tmuxCmd("capture-pane", "-e", "-p", "-t", pane).Output()
	return string(b), e
}

// Called with the input and phone locks held. Reserve the target before returning
// pending; only the delayed Enter and observation run outside those locks.
func (s *remoteInputStore) beginSubmit(route string, m remote.Message, h *remoteInputHistory, now time.Time, retry bool) remote.Message {
	result := remote.Message{Type: "input_result", ID: m.ID, Status: "refused"}
	target := remoteSubmission{pane: m.Pane, until: now.Add(2 * time.Minute)}
	text := ""
	if retry {
		ref, ok := m.Data.(string)
		if !ok || len(ref) > 128 {
			result.Reason = "invalid original input id"
			return result
		}
		original, ok := h.submits[ref]
		if !ok || original.used || now.After(original.until) || original.pane != m.Pane || original.fingerprint == "" {
			result.Reason = "original draft is no longer available for Retry Enter"
			return result
		}
		target = original
		if err := checkRemoteDraft(target); err != nil {
			result.Reason = err.Error()
			return result
		}
		original.used = true
		h.submits[ref] = original
		audit := remote.Message{Type: "input", ID: m.ID, Kind: "key", Pane: m.Pane, Data: "Enter"}
		if err := remoteInputAudit(route, audit, now); err != nil {
			result.Reason = "input audit: " + err.Error()
			return result
		}
	} else {
		text = m.Data.(string)
		identity, err := remotePaneIdentity(m.Pane)
		if err != nil {
			result.Reason = err.Error()
			return result
		}
		target.identity = identity
		if err := insertRemoteInput(m); err != nil {
			result.Reason = "tmux failed; delivery may be partial: " + err.Error()
			return result
		}
	}
	if s.submitting == nil {
		s.submitting = map[string]string{}
	}
	s.submitting[m.Pane] = route + ":" + m.ID
	result.Status = "pending"
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.finishSubmit(route, m.ID, target, text) }()
	return result
}

func remotePaneIdentity(pane string) (string, error) {
	b, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_pid}\t#{session_name}").Output()
	fields := strings.Split(strings.TrimSpace(string(b)), "\t")
	if err != nil || len(fields) != 2 || fields[0] == "" || (fields[1] != tmuxSessionName && fields[1] != contractorSession) {
		return "", errors.New("pane unavailable or outside Partyline")
	}
	return fields[0], nil
}

func checkRemoteDraft(target remoteSubmission) error {
	identity, err := remotePaneIdentity(target.pane)
	if err != nil || identity != target.identity {
		return errors.New("pane changed; no Enter sent")
	}
	screen, err := remoteSubmitSnapshot(target.pane)
	if err != nil || remoteComposerFingerprint(screen) == "" || remoteComposerFingerprint(screen) != target.fingerprint {
		return errors.New("draft changed; no Enter sent")
	}
	return nil
}

func (s *remoteInputStore) finishSubmit(route, id string, target remoteSubmission, text string) {
	remoteSubmitWait(300 * time.Millisecond)
	err := withPhoneLock(func() error {
		phones, e := remotePhones()
		if e != nil {
			return e
		}
		known := false
		for _, phone := range phones {
			if phone.ID == route {
				known = true
				break
			}
		}
		if !known {
			return errors.New("phone revoked")
		}
		identity, e := remotePaneIdentity(target.pane)
		if e != nil || identity != target.identity {
			return errors.New("pane changed")
		}
		if target.fingerprint != "" && time.Now().After(target.until) {
			return errors.New("Retry Enter expired; no Enter sent")
		}
		if target.fingerprint != "" {
			if e := checkRemoteDraft(target); e != nil {
				return e
			}
		}
		return tmuxCmd("send-keys", "-t", target.pane, "--", "Enter").Run()
	})
	phase := "delivered"
	if err != nil {
		phase = "failed"
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	if auditErr := remoteOutcomeAudit(route, id, target.pane, phase, reason); auditErr != nil {
		err = fmt.Errorf("outcome audit: %w", auditErr)
	}
	result := remote.Message{Type: "input_result", ID: id, Status: "ok", Data: map[string]any{"inputDelivery": "delivered"}}
	if err != nil {
		result.Status = "refused"
		result.Data = nil
		result.Reason = fmt.Sprintf("delivery may be partial: %v", err)
	}
	s.mu.Lock()
	if s.submitting[target.pane] == route+":"+id {
		delete(s.submitting, target.pane)
	}
	s.publishLocked(route, result)
	s.mu.Unlock()
	if err != nil {
		return
	}
	remoteSubmitWait(time.Second)
	identity, identityErr := remotePaneIdentity(target.pane)
	if identityErr != nil || identity != target.identity {
		return
	}
	screen, err := remoteSubmitSnapshot(target.pane)
	if err != nil {
		return
	}
	phase, fingerprint := remoteDeliveryState(screen, text, target.fingerprint)
	if err := remoteOutcomeAudit(route, id, target.pane, phase, ""); err != nil {
		return
	}
	data := map[string]any{"inputDelivery": phase}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.phones[route]
	if h == nil {
		return
	}
	if _, ok := h.results[id]; !ok {
		return
	}
	if fingerprint != "" {
		if h.submits == nil {
			h.submits = map[string]remoteSubmission{}
		}
		target.fingerprint = fingerprint
		target.until = time.Now().Add(2 * time.Minute)
		target.used = false
		h.submits[id] = target
		data["retryInputId"] = id
	}
	result.Type = "input_delivery"
	result.Data = data
	s.publishLocked(route, result)
}
