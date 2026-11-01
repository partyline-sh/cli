package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"partyline.sh/partyline/internal/remote"
)

// Live input belongs to one encrypted connection. Losing that connection loses
// all tokens, sequence numbers and unsent input; it is never resumable.
const liveLease = 45 * time.Second
const liveSettle = 150 * time.Millisecond // eligibility to reject a prediction, not proof of execution

type liveWrite struct {
	seq uint64
	at  time.Time
}

type liveTerminal struct {
	completions                   []liveWrite
	totalBatches, totalBytes      uint64
	token, openID, pane, identity string
	until, sent, written          time.Time
	accepted, echo, revision      uint64
	tokens, bytes                 float64
	budgetAt                      time.Time
	previous                      string
}

type liveOpenBudget struct {
	tokens float64
	at     time.Time
}
type liveStore struct {
	fast     bool // only explicitly self-hosted relays use the fast screen cadence
	sessions map[string]*liveTerminal
	opens    map[string]*liveOpenBudget
}

func (s *liveStore) command(route string, m remote.Message, now time.Time) remote.Message {
	result := remote.Message{Type: "live_result", ID: m.ID, Pane: m.Pane, Status: "refused"}
	err := withPhoneLock(func() error {
		phones, err := remotePhones()
		if err != nil {
			return err
		}
		known := false
		for _, p := range phones {
			if p.ID == route {
				known = true
			}
		}
		if !known {
			return errors.New("unknown or revoked phone")
		}
		if m.ID == "" || len(m.ID) > 128 {
			return errors.New("invalid live input id")
		}
		if s.sessions == nil {
			s.sessions = map[string]*liveTerminal{}
		}
		l := s.sessions[route]
		if m.Type == "live_open" {
			// Repeating an open request on this connection is harmless.
			if l != nil && l.openID == m.ID && l.pane == m.Pane && now.Before(l.until) {
				result.Data = map[string]any{"token": l.token}
				result.Status = "ok"
				return nil
			}
			if s.opens == nil {
				s.opens = map[string]*liveOpenBudget{}
			}
			budget := s.opens[route]
			if budget == nil {
				budget = &liveOpenBudget{tokens: 4, at: now}
				s.opens[route] = budget
			}
			budget.tokens = math.Min(4, budget.tokens+math.Max(0, now.Sub(budget.at).Seconds()))
			budget.at = now
			if budget.tokens < 1 {
				return errors.New("wait before reopening the terminal")
			}
			budget.tokens--
			for owner, other := range s.sessions {
				if owner != route && other.pane == m.Pane && now.Before(other.until) {
					return errors.New("another phone is typing in this terminal")
				}
			}
			identity, _, err := livePaneState(m.Pane)
			if err != nil {
				return err
			}
			var nonce [24]byte
			if _, err = rand.Read(nonce[:]); err != nil {
				return err
			}
			token := hex.EncodeToString(nonce[:])
			if err = liveAudit(route, m.Pane, "open"); err != nil {
				return err
			}
			l = &liveTerminal{token: token, openID: m.ID, pane: m.Pane, identity: identity, until: now.Add(liveLease), tokens: 60, bytes: 64 * 1024, budgetAt: now}
			if old := s.sessions[route]; old != nil {
				s.end(route, "replaced")
			}
			s.sessions[route] = l
			result.Data = map[string]any{"token": token}
			result.Status = "ok"
			return nil
		}
		if l == nil || m.ID != l.token || m.Pane != l.pane || !now.Before(l.until) {
			return errors.New("live session expired; reopen the terminal")
		}
		if m.Type == "live_close" {
			s.end(route, "close")
			result.Status = "ok"
			return nil
		}
		identity, foreground, err := livePaneState(m.Pane)
		if err != nil || identity != l.identity {
			return errors.New("shell target changed or foreground program is unsupported; reopen the terminal")
		}
		l.until = now.Add(liveLease)
		if m.Type == "live_ping" {
			result.Status = "ok"
			return nil
		}
		if m.Type != "live_input" {
			return errors.New("unsupported live operation")
		}
		if m.Revision <= l.accepted {
			result.Status = "ok"
			result.Revision = l.accepted
			return nil
		}
		if m.Revision != l.accepted+1 {
			return errors.New("live input order lost; reopen without replaying input")
		}
		if !liveForeground[foreground] && !(m.Kind == "key" && (m.Data == "C-c" || m.Data == "C-z")) {
			return errors.New(foreground + " is running; only Ctrl-C and Ctrl-Z are sent")
		}
		data, ok := m.Data.(string)
		if !ok || data == "" || len(data) > 2048 || !utf8.ValidString(data) {
			return errors.New("invalid live input")
		}
		switch m.Kind {
		case "text", "paste":
			for _, r := range data {
				if unicode.IsControl(r) && !(m.Kind == "paste" && (r == '\n' || r == '\t')) {
					return errors.New("control bytes are not literal text")
				}
			}
		case "key":
			if !liveKeys[data] {
				return errors.New("unsupported terminal key")
			}
		default:
			return errors.New("unsupported live input kind")
		}
		elapsed := math.Max(0, now.Sub(l.budgetAt).Seconds())
		l.tokens = math.Min(60, l.tokens+elapsed*30)
		l.bytes = math.Min(64*1024, l.bytes+elapsed*16*1024)
		l.budgetAt = now
		if len(l.completions) >= 256 || l.tokens < 1 || l.bytes < float64(len(data)) {
			return errors.New("live input budget exceeded; pending input discarded")
		}
		l.tokens--
		l.bytes -= float64(len(data))
		// Reserve the sequence before writing: a partial tmux failure is never retried.
		l.accepted = m.Revision
		if m.Kind == "key" {
			err = tmuxCmd("send-keys", "-t", l.pane, "--", data).Run()
		} else {
			err = insertRemoteInput(m)
		}
		if err != nil {
			return errors.New("terminal write failed; delivery may be partial; input will not be replayed")
		}
		l.totalBatches++
		l.totalBytes += uint64(len(data))
		l.written = time.Now()
		l.completions = append(l.completions, liveWrite{l.accepted, l.written})

		result.Status, result.Revision = "ok", l.accepted
		return nil
	})
	if err != nil {
		if l := s.sessions[route]; l != nil && m.ID == l.token {
			s.end(route, "refused")
		}
		result.Reason = err.Error()
	}
	return result
}

// This is a shell UX scope, not an agent approval boundary. The declared engine
// and foreground are checked before each burst, with a check/write race and no
// visibility into editor-embedded terminals (for example vim :terminal).
func livePaneState(pane string) (string, string, error) {
	if !strings.HasPrefix(pane, "%") || len(pane) > 20 {
		return "", "", errors.New("invalid pane")
	}
	for _, r := range pane[1:] {
		if r < '0' || r > '9' {
			return "", "", errors.New("invalid pane")
		}
	}
	out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_id}\t#{session_name}\t#{pane_pid}\t#{pane_current_command}\t#{@ptln_key}\t#{@ptln_spec}").Output()
	if err != nil {
		return "", "", err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\t")
	if len(fields) != 6 || fields[0] != pane || fields[1] != tmuxSessionName && fields[1] != contractorSession {
		return "", "", errors.New("shell pane unavailable")
	}
	sp, valid := decodePaneSpec(fields[5])
	if fields[5] != "" && !valid {
		return "", "", errors.New("pane spec unavailable")
	}
	switch strings.ToLower(strings.TrimSpace(sp.Engine)) {
	case "", "shell", "sh", "bash", "zsh", "fish", "pwsh", "powershell":
	default:
		return "", "", errors.New("direct typing is available only in shell sessions")
	}
	foreground := strings.ToLower(strings.TrimPrefix(fields[3], "-"))
	switch foreground {
	case "claude", "codex", "gemini", "opencode", "aider", "goose", "ssh", "mosh":
		return "", "", errors.New("agent or remote-shell foreground is outside direct shell input")
	}
	return fields[2] + ":" + fields[4], foreground, nil
}

var liveForeground = func() map[string]bool {
	result := map[string]bool{}
	for _, name := range strings.Fields("sh bash zsh fish pwsh powershell vim nvim vi nano less more man top htop") {
		result[name] = true
	}
	return result
}()

// Audit session boundaries, never typed content, per-key timing or password length.
// The durable open intent must succeed before a token can accept keystrokes.
func liveAudit(route, pane, action string, totals ...uint64) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	entry := map[string]any{"at": time.Now().UTC(), "action": "live_" + action, "phoneId": route, "pane": pane}
	if len(totals) == 2 {
		entry["totalBatches"] = totals[0]
		entry["totalBytes"] = totals[1]
	}
	err = json.NewEncoder(f).Encode(entry)
	if err != nil {
		return err
	}
	return f.Sync()
}

var liveKeys = func() map[string]bool {
	keys := map[string]bool{}
	for _, key := range strings.Fields("Escape Enter Tab BTab BSpace Up Down Left Right Home End DC PageUp PageDown C-a C-b C-c C-d C-e C-f C-k C-l C-n C-p C-r C-u C-w C-z") {
		keys[key] = true
	}
	return keys
}()

func (s *liveStore) end(route, reason string) {
	if l := s.sessions[route]; l != nil {
		delete(s.sessions, route)
		_ = liveAudit(route, l.pane, reason, l.totalBatches, l.totalBytes)
	}
}
