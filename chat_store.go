package main

// PARTYLINE CHAT: one room on this machine where the person and their running agent sessions talk.
//
// The room is an append-only log of messages plus a member list, both under ~/.partyline/chat/.
// Nothing about delivery types into an agent's input box. A message reaches an agent through its
// own hooks (chat_hook.go): a session that is working gets it the moment its turn ends, and an
// idle Claude session gets it through a background `ptln chat listen` that exits when something
// arrives, which Claude Code answers by waking the session. Both were checked against Claude
// Code 2.1.310 before this was written.
//
// All writes take one lock file, because the TUI, every member's hooks and every listener write
// concurrently, and a lost update here is a message nobody receives.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type chatMessage struct {
	Seq  int       `json:"seq"`
	At   time.Time `json:"at"`
	From string    `json:"from"`         // member key, or "you" for the person
	Name string    `json:"name"`         // how it is shown: "you", "#11 claude"
	To   []string  `json:"to,omitempty"` // member keys; empty = everyone
	Text string    `json:"text"`
	Note bool      `json:"note,omitempty"` // partyline's own notice, never delivered to agents
}

type chatMember struct {
	Key    string `json:"key"`
	Name   string `json:"name"`   // "#11 claude"
	Engine string `json:"engine"` // claude | codex
	Read   int    `json:"read"`   // highest seq delivered to this member
	// Answering is set when messages were handed to the member, so the reply that ends its next
	// turn is posted to the room — and only that reply, not whatever the session does after.
	Answering bool `json:"answering,omitempty"`
	// Sent is set when the member replied with `ptln chat send` during that turn, so the Stop
	// hook does not post its final text as a second copy.
	Sent   bool      `json:"sent,omitempty"`
	Nudged time.Time `json:"nudged,omitempty"` // last time it was asked to start its listener
	// Active is the last time it received, sent, or started listening. A session that did any of
	// those in the last minute is in the middle of the conversation and is never woken by typing.
	Active time.Time `json:"active,omitempty"`
	// Delivered is when messages were last handed to it, for the "thinking… 12s" line in the room.
	Delivered time.Time `json:"delivered,omitempty"`
	// Paused is set by /stop <tab>: messages for this member wait until /go <tab>.
	Paused bool      `json:"paused,omitempty"`
	Joined time.Time `json:"joined"`
}

type chatRoom struct {
	Members []chatMember `json:"members"`
	// AgentRun counts messages agents have posted since the person last spoke. Agents answering
	// each other is the point, but it must not run unattended for ever; at chatAgentRunCap the
	// room stops delivering to agents until the person says something.
	AgentRun int `json:"agent_run"`
	// Stopped is set by /stop with no arguments: nothing is delivered to any member until /go or
	// the person posts again.
	Stopped bool `json:"stopped,omitempty"`
}

const chatAgentRunCap = 16

func chatDir() string            { return filepath.Join(stateDir(), "chat") }
func chatLogPath() string        { return filepath.Join(chatDir(), "room.jsonl") }
func chatRoomPath() string       { return filepath.Join(chatDir(), "room.json") }
func chatLockPath() string       { return filepath.Join(chatDir(), ".lock") }
func (r *chatRoom) paused() bool { return r.AgentRun >= chatAgentRunCap || r.Stopped }

// held reports whether delivery to this member is stopped, for the room or for it alone.
func (r *chatRoom) held(m chatMember) bool { return r.paused() || m.Paused }

// withChatLock runs fn holding the room lock, with the room loaded and saved around it.
func withChatLock(fn func(r *chatRoom) error) error {
	if err := os.MkdirAll(chatDir(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(chatLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	var r chatRoom
	if b, err := os.ReadFile(chatRoomPath()); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	if err := fn(&r); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := chatRoomPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, chatRoomPath())
}

// loadChatRoom reads the room without locking or writing it, for the room's screen. room.json is
// replaced by rename, so a read sees a whole version.
func loadChatRoom() chatRoom {
	var r chatRoom
	if b, err := os.ReadFile(chatRoomPath()); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

func (r *chatRoom) member(key string) *chatMember {
	for i := range r.Members {
		if r.Members[i].Key == key {
			return &r.Members[i]
		}
	}
	return nil
}

// readChat returns every message, oldest first.
func readChat() []chatMessage {
	f, err := os.Open(chatLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []chatMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m chatMessage
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// postLocked appends a message. The caller holds the lock (seq must be assigned under it).
func postLocked(r *chatRoom, m chatMessage) (chatMessage, error) {
	msgs := readChat()
	m.Seq = 1
	if len(msgs) > 0 {
		m.Seq = msgs[len(msgs)-1].Seq + 1
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	switch {
	case m.Note:
	case m.From == "you":
		r.AgentRun, r.Stopped = 0, false
	default:
		r.AgentRun++
	}
	b, _ := json.Marshal(m)
	f, err := os.OpenFile(chatLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return m, err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return m, err
}

// unreadFor is what the room still owes a member: messages after its read mark, not its own, not
// notes, and addressed to everyone or to it.
func unreadFor(msgs []chatMessage, m chatMember) []chatMessage {
	var out []chatMessage
	for _, x := range msgs {
		if x.Seq <= m.Read || x.From == m.Key || x.Note {
			continue
		}
		if len(x.To) > 0 && !containsStr(x.To, m.Key) {
			continue
		}
		out = append(out, x)
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// chatMentionTag normalizes a standalone mention token. A bare @ (including trailing
// punctuation) is ordinary text, so it never changes the recipient list.
func chatMentionTag(word string) string {
	if !strings.HasPrefix(word, "@") {
		return ""
	}
	return strings.ToLower(strings.TrimRight(strings.TrimPrefix(word, "@"), ",.:;!?)]}\"'`"))
}

// chatHasMention reports whether text targets sessions without an explicit @all broadcast.
func chatHasMention(text string) bool {
	mentioned := false
	for _, w := range strings.Fields(text) {
		tag := chatMentionTag(w)
		if tag == "all" {
			return false
		}
		mentioned = mentioned || tag != ""
	}
	return mentioned
}

// chatAddressees resolves @mentions in text to member keys. "@11" matches "#11 …", "@codex" matches
// the engine. No mention, or @all, means everyone.
func chatAddressees(r *chatRoom, text string) []string {
	var to []string
	for _, w := range strings.Fields(text) {
		tag := chatMentionTag(w)
		if tag == "" {
			continue
		}
		if tag == "all" {
			return nil
		}
		for _, m := range r.Members {
			num := strings.TrimPrefix(strings.Fields(m.Name + " ")[0], "#")
			if tag == num || tag == m.Engine || tag == strings.ToLower(m.Name) {
				if !containsStr(to, m.Key) {
					to = append(to, m.Key)
				}
			}
		}
	}
	return to
}
