package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"partyline.sh/partyline/internal/phoneterm"
	"partyline.sh/partyline/internal/remote"
	"partyline.sh/partyline/internal/terminalstream"
)

func phoneTerminalRegistry() phoneterm.Registry {
	return phoneterm.Registry{Root: filepath.Join(relayDir(), "terminals"), Socket: tmuxSocketName() + "-phone"}
}
func terminalStreamAudit(owner, id, action string) error {
	file, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(map[string]any{"at": time.Now().UTC(), "action": "term_" + action, "phoneId": owner, "session": id})
}
func (c *remoteChannel) terminalCommand(owner string, msg remote.Message) remote.Message {
	if c.term == nil {
		c.term = terminalstream.New(phoneTerminalRegistry(), terminalStreamAudit, terminalPhoneAllowed)
		c.termEvents = c.term.Events
	}
	if err := c.term.Submit(owner, msg); err != nil {
		return remote.Message{Type: "term_result", ID: msg.ID, Pane: msg.Pane, Status: "refused", Reason: err.Error()}
	}
	return remote.Message{}
}
func (c *remoteChannel) terminalFrame(event terminalstream.Event) ([]byte, error) {
	peer := c.peers[event.Owner]
	if peer == nil {
		return nil, nil
	}
	allowed := c.termACL.allowed(event.Owner, time.Now())
	if !allowed {
		c.drop(event.Owner)
		return nil, nil
	}
	wire, err := peer.EncryptImmediate(event.Message)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(event.Owner)
	if err != nil || len(raw) != 16 {
		return nil, errors.New("invalid phone route")
	}
	var route [16]byte
	copy(route[:], raw)
	return remote.Batch(map[[16]byte][]byte{route: wire}), nil
}

func phoneTerminalRows() []aiSession {
	registry := phoneTerminalRegistry()
	if _, err := os.Stat(filepath.Join(registry.Root, "sessions.json")); err != nil {
		return nil
	}
	sessions, err := registry.List("")
	if err != nil {
		return nil
	}
	var result []aiSession
	for _, session := range sessions {
		result = append(result, aiSession{Tool: "phone", ID: session.ID, Title: session.Title, LastActive: session.LastAttached})
	}
	return result
}

func terminalPhoneAllowed(owner string) bool {
	phones, err := remotePhones()
	if err != nil {
		return false
	}
	for _, phone := range phones {
		if phone.ID == owner {
			return true
		}
	}
	return false
}

func (c *remoteChannel) terminalControls(owner string) ([]byte, error) {
	peer := c.peers[owner]
	if peer == nil {
		return nil, nil
	}
	wire, err := peer.FlushTerminalControl()
	if err != nil || len(wire) == 0 {
		return nil, err
	}
	raw, err := hex.DecodeString(owner)
	if err != nil || len(raw) != 16 {
		return nil, errors.New("invalid phone route")
	}
	var route [16]byte
	copy(route[:], raw)
	return remote.Batch(map[[16]byte][]byte{route: wire}), nil
}

var phoneTerminalCleanupState struct {
	sync.Mutex
	last    time.Time
	running bool
}

// Expiry runs independently of active phone connections. Filesystem/process work
// stays off the watcher's status loop and never overlaps another expiry pass.
func cleanupPhoneTerminals() {
	state := &phoneTerminalCleanupState
	state.Lock()
	if state.running || time.Since(state.last) < time.Minute {
		state.Unlock()
		return
	}
	registry := phoneTerminalRegistry()
	if _, err := os.Stat(filepath.Join(registry.Root, "sessions.json")); err != nil {
		state.Unlock()
		return
	}
	state.running = true
	state.last = time.Now()
	state.Unlock()
	go func() {
		defer func() { state.Lock(); state.running = false; state.Unlock() }()
		_ = registry.Cleanup(time.Now())
	}()
}

// Cache output-route checks only. Incoming requests still re-read pairing state,
// and the stream worker revalidates it before process operations.
type terminalPhoneCache struct {
	sync.Mutex
	until  time.Time
	phones map[string]bool
}

func (c *terminalPhoneCache) allowed(owner string, now time.Time) bool {
	c.Lock()
	defer c.Unlock()
	if !now.Before(c.until) {
		c.until = now.Add(time.Second)
		phones, err := remotePhones()
		if err == nil {
			// A transient read error must not disconnect a live terminal. Only
			// replace the last successful set after a successful pairing read.
			c.phones = map[string]bool{}
			for _, phone := range phones {
				c.phones[phone.ID] = true
			}
		}
	}
	return c.phones[owner]
}
