package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"partyline.sh/partyline/internal/remote"
)

var remoteInputKeys = strings.Fields("Escape Enter Tab BTab BSpace Up Down Left Right PageUp PageDown C-c C-d C-r C-l")

type remoteInputHistory struct {
	results map[string]remote.Message
	order   []string
	tokens  float64
	last    time.Time
	submits map[string]remoteSubmission
}
type remoteInputStore struct {
	mu         sync.Mutex
	phones     map[string]*remoteInputHistory
	challenges map[string]remoteLoopChallenge
	submitting map[string]string
	mail       map[string][]remote.Message
	workers    sync.WaitGroup
}

// Process lifetime: survives both phone and machine transport reconnects.
var processRemoteInputs = &remoteInputStore{phones: map[string]*remoteInputHistory{}}

func (s *remoteInputStore) handle(route string, m remote.Message, now time.Time) remote.Message {
	return s.handleMode(route, m, now, false)
}

func (s *remoteInputStore) handleAsync(route string, m remote.Message, now time.Time) remote.Message {
	return s.handleMode(route, m, now, true)
}

func (s *remoteInputStore) handleMode(route string, m remote.Message, now time.Time, async bool) remote.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := remote.Message{Type: "input_result", ID: m.ID, Status: "refused"}
	// Serialize authorization and audit with revoke, including callers outside receive.
	err := withPhoneLock(func() error {
		phones, err := remotePhones()
		if err != nil {
			return err
		}
		known := false
		for _, p := range phones {
			if p.ID == route {
				known = true
				break
			}
		}
		if !known {
			return errors.New("unknown or revoked phone")
		}
		if m.ID == "" || len(m.ID) > 128 {
			return errors.New("invalid input id")
		}
		h := s.phones[route]
		if h == nil {
			h = &remoteInputHistory{results: map[string]remote.Message{}, tokens: 40, last: now}
			s.phones[route] = h
		}
		if old, ok := h.results[m.ID]; ok {
			result = old
			return nil
		}
		// Cache effects, including failures: a retry must return the original outcome.
		// Read/prepare responses carry large details and do not execute actions. Not a rate-limit refusal:
		// nothing ran, and a retry reuses the id, so recording it would refuse that input forever.
		limited := false
		defer func() {
			if limited || m.Type == "loop_detail" || m.Type == "loop_prepare" || m.Type == "session_list" || m.Type == "terminal_history" {
				return
			}
			h.results[m.ID] = result
			h.order = append(h.order, m.ID)
			if len(h.order) > 1000 {
				delete(h.results, h.order[0])
				delete(h.submits, h.order[0])
				h.order = h.order[1:]
			}
		}()
		refuse := func(err error) error { result.Reason = err.Error(); return nil }
		elapsed := math.Max(0, now.Sub(h.last).Seconds())
		h.tokens = math.Min(40, h.tokens+elapsed*20)
		h.last = now
		// Chunks are limited by bytes in the upload store (remote_upload.go): at 20 a second a
		// 20 MB file of 32 KB chunks would take half a minute of refusals.
		if m.Type != "upload_chunk" {
			if h.tokens < 1 {
				limited = true
				return refuse(errors.New("rate limit exceeded"))
			}
			h.tokens--
		}
		if m.Type == "input" || m.Type == "input_submit" {
			if s.submitting[m.Pane] != "" {
				return refuse(errors.New("a previous message is still being submitted to this pane"))
			}
		}
		if m.Type == "input_submit" {
			result = s.beginSubmit(route, m, h, now, true)
			return nil
		}
		if m.Type == "terminal_close" {
			result.Status, result.Reason, result.Data = remoteTerminalClose(route, m, now)
			return nil
		}
		if m.Type == "terminal_history" {
			result.Status, result.Reason, result.Data = remoteTerminalHistory(m)
			return nil
		}
		if m.Type == "session_list" || m.Type == "session_open" || m.Type == "session_close" {
			result.Status, result.Reason, result.Data = remoteSessionControl(route, m, now)
			return nil
		}
		if m.Type == "shell_open" || m.Type == "loop_detail" || m.Type == "loop_prepare" || m.Type == "loop_action" {
			result.Status, result.Reason, result.Data = s.control(route, m, now)
			return nil
		}
		if m.Type == "resize" {
			result.Status, result.Reason = processResizes.handle(route, m, now)
			return nil
		}
		if strings.HasPrefix(m.Type, "upload_") {
			result.Status, result.Reason, result.Data = processUploads.handle(route, m, now)
			if result.Reason == "upload rate limit; resend this chunk" {
				limited = true // nothing was written, and the retry reuses the id
			}
			return nil
		}
		if m.Type == "chat_limit" {
			result.Status, result.Reason, result.Data = remoteChatLimit(route, m, now)
			return nil
		}
		if err := validateRemoteInput(m); err != nil {
			return refuse(err)
		}
		if m.Type == "input" {
			inventory, err := remoteSnapshot("sessions", "focus")
			if err != nil {
				return refuse(err)
			}
			found := false
			for _, row := range inventory.([]map[string]string) {
				if row["pane"] == m.Pane {
					found = true
					break
				}
			}
			if !found {
				return refuse(errors.New("pane unavailable"))
			}
			// Write intent before effects; audit failure must prevent unaudited typing.
			if err := remoteInputAudit(route, m, now); err != nil {
				return refuse(fmt.Errorf("input audit: %w", err))
			}
			if async && m.Kind == "text" {
				result = s.beginSubmit(route, m, h, now, false)
				return nil
			}
			err = runRemoteInput(m)
			if err != nil {
				return refuse(fmt.Errorf("tmux failed; delivery may be partial: %w", err))
			}
		} else {
			err := chatSendPerson(m.Text)
			if err != nil {
				return refuse(fmt.Errorf("chat failed; delivery may be partial: %w", err))
			}
		}
		result.Status = "ok"
		return nil
	})
	if err != nil {
		result.Reason = err.Error()
	}
	return result
}

func validateRemoteInput(m remote.Message) error {
	if len(m.Target) > 160 {
		return errors.New("invalid input target")
	}
	data, ok := m.Data.(string)
	if m.Type == "chat_send" {
		data, ok = m.Text, true
	} else if m.Type != "input" {
		return errors.New("unsupported input")
	}
	if !ok {
		return errors.New("data must be text")
	}
	if len(data) > remote.MaxInputBytes {
		return errors.New("payload exceeds 24 KiB")
	}
	if !utf8.ValidString(data) {
		return errors.New("invalid UTF-8")
	}
	for _, r := range data {
		if unicode.IsControl(r) && r != '\t' && r != '\n' {
			return errors.New("control character refused")
		}
	}
	encoded, err := json.Marshal([]remote.Message{m})
	if err != nil || len(encoded)+16 > remote.MaxCiphertextBytes {
		return errors.New("encoded input exceeds transport frame")
	}
	if m.Type == "chat_send" {
		if strings.TrimSpace(data) == "" {
			return errors.New("empty message")
		}
		return nil
	}
	switch m.Kind {
	case "text", "paste":
	case "key":
		for _, key := range remoteInputKeys {
			if key == data {
				return nil
			}
		}
		return errors.New("key not allowed")
	default:
		return errors.New("input kind not allowed")
	}
	return nil
}
