package main

import (
	"encoding/json"
	"os"
	"partyline.sh/partyline/internal/remote"
	"path/filepath"
	"time"
)

func remoteInputAudit(route string, m remote.Message, now time.Time) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	err = json.NewEncoder(f).Encode(struct {
		At      time.Time `json:"at"`
		Action  string    `json:"action"`
		ID      string    `json:"inputId"`
		PhoneID string    `json:"phoneId"`
		Pane    string    `json:"pane"`
		Kind    string    `json:"kind"`
		Size    int       `json:"size"`
	}{now.UTC(), "input", m.ID, route, m.Pane, m.Kind, len(m.Data.(string))})
	if err != nil {
		return err
	}
	return f.Sync()
}

// Record observation/submit outcomes without copying prompt text into the audit.
func remoteOutcomeAudit(route, id, pane, state, reason string) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	err = json.NewEncoder(f).Encode(struct {
		At     time.Time `json:"at"`
		Action string    `json:"action"`
		ID     string    `json:"inputId"`
		Phone  string    `json:"phoneId"`
		Pane   string    `json:"pane"`
		State  string    `json:"state"`
		Reason string    `json:"reason,omitempty"`
	}{time.Now().UTC(), "input_outcome", id, route, pane, state, reason})
	if err != nil {
		return err
	}
	return f.Sync()
}
