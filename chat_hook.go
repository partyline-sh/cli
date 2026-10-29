package main

// How chat messages reach a session and how its replies come back, through its own hooks.
//
//   - Stop hook (`ptln chat-hook`, wired per session in sessionSettings): when a member finishes a
//     turn and anything is waiting for it, the hook blocks the stop with those messages as the
//     reason, and the session answers them straight away with `ptln chat send`.
//   - Idle sessions: no hook fires while a session waits for input. A Claude member keeps
//     `ptln chat listen` running as a background task; it exits when a message arrives, and Claude
//     Code wakes the session with its output. The Stop hook sees the session's running background
//     tasks, so a member whose listener is not running is asked to start it.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// chatSelfKeys are every identity this process could have in the room: the partyline session key
// the pane was started with, its tmux pane, and the engine's own session id. The room may know a
// member by any of them (a session started by hand inside a pane has no partyline key), so
// matching on one and not the others left the first interactive test saying "not in the chat".
// Codex drops variables whose names contain KEY from the commands it runs, so PARTYLINE_SESSION_KEY
// never reaches them; it passes its session id, which partyline launched it with as that key, as
// CODEX_THREAD_ID.
func chatSelfKeys(sessionID string) []string {
	var keys []string
	for _, k := range []string{os.Getenv("PARTYLINE_SESSION_KEY"), os.Getenv("CODEX_THREAD_ID"), chatSelfPaneKey(), sessionID} {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// self finds this process's membership by any of its identities.
func (r *chatRoom) self(keys []string) *chatMember {
	for _, k := range keys {
		if m := r.member(k); m != nil {
			return m
		}
	}
	return nil
}

// chatDeliveryText is what a member is handed: the new messages, then how replying works.
func chatDeliveryText(self chatMember, msgs []chatMessage, r *chatRoom) string {
	var b strings.Builder
	b.WriteString("[partyline chat] New messages in the room:\n\n")
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s: %s\n", m.Name, m.Text)
	}
	var others []string
	for _, m := range r.Members {
		if m.Key != self.Key {
			others = append(others, m.Name)
		}
	}
	fmt.Fprintf(&b, "\nYou are %s. Also in the room: the person, %s.\n", self.Name, strings.Join(others, ", "))
	b.WriteString("Reply by running `ptln chat send \"...\"` (put @<number> or @<engine> in it to address one participant). ")
	b.WriteString("Write it as a short chat message in normal sentences, not a report; longer only if you are asked for detail. ")
	b.WriteString("Markdown is rendered (lists, **bold**, `code`, code blocks, tables), so use it instead of capitalised labels; skip headings in short replies. ")
	b.WriteString("Use tools first if you need to, and send nothing if you have nothing to add. ")
	if self.Engine == "claude" {
		b.WriteString("Then run `ptln chat listen` with the Bash tool using run_in_background: true, so later messages reach you while you are idle.")
	} else {
		b.WriteString("Later messages are delivered to you the same way.")
	}
	b.WriteString("\n")
	return b.String()
}

const chatListenNudge = "[partyline chat] You are in the partyline chat but nothing is listening for messages while you are idle. " +
	"Run `ptln chat listen` with the Bash tool using run_in_background: true, then stop. Do not reply to the room about this."

const chatNudgeEvery = 10 * time.Minute

type chatStopInput struct {
	SessionID       string `json:"session_id"`
	StopHookActive  bool   `json:"stop_hook_active"`
	BackgroundTasks []struct {
		Status  string `json:"status"`
		Command string `json:"command"`
	} `json:"background_tasks"`
}

func (in chatStopInput) listening() bool {
	for _, t := range in.BackgroundTasks {
		if t.Status == "running" && strings.Contains(t.Command, "chat listen") {
			return true
		}
	}
	return false
}

// chatStopDecide is the Stop hook's logic, separated for tests. It returns the block reason, or ""
// to let the session stop.
func chatStopDecide(r *chatRoom, keys []string, in chatStopInput, now time.Time) (string, error) {
	me := r.self(keys)
	if me == nil {
		return "", nil
	}
	// A reply is whatever the session sends with `ptln chat send`. Its final turn text is NOT
	// posted on its behalf: when a session decides it has nothing to add, that text is narration
	// ("nothing to send"), and posting it woke the other members to narrate back. Seen in the first
	// end-to-end run, which is why this hook only delivers.
	me.Answering, me.Sent = false, false
	if !r.held(*me) {
		if msgs := unreadFor(readChat(), *me); len(msgs) > 0 {
			me.Read, me.Answering, me.Sent, me.Active, me.Delivered = msgs[len(msgs)-1].Seq, true, false, now, now
			return chatDeliveryText(*me, msgs, r), nil
		}
	}
	if me.Engine == "claude" && !in.listening() && !in.StopHookActive && now.Sub(me.Nudged) > chatNudgeEvery {
		me.Nudged = now
		return chatListenNudge, nil
	}
	return "", nil
}

// chatHookMain is `ptln chat-hook`, the Stop hook. Silent and fail-open: any error lets the session
// stop normally.
func chatHookMain() {
	ensureWatcher()
	var in chatStopInput
	if b, _ := io.ReadAll(os.Stdin); len(b) > 0 {
		_ = json.Unmarshal(b, &in)
	}
	keys := chatSelfKeys(in.SessionID)
	if len(keys) == 0 {
		return
	}
	// A loop's messages first (loop_inbox.go): this is the Stop hook every partyline-launched
	// Claude session already has, so loops reach running sessions without a restart.
	if reason := loopStopReason(keys); reason != "" {
		out, _ := json.Marshal(map[string]any{"decision": "block", "reason": reason})
		fmt.Println(string(out))
		return
	}
	if _, err := os.Stat(chatRoomPath()); err != nil {
		return // no room on this machine: the common case, and it costs one stat
	}
	reason := ""
	if withChatLock(func(r *chatRoom) error {
		var err error
		reason, err = chatStopDecide(r, keys, in, time.Now())
		return err
	}) != nil || reason == "" {
		return
	}
	out, _ := json.Marshal(map[string]any{"decision": "block", "reason": reason})
	fmt.Println(string(out))
}

// chatListen is `ptln chat listen`: wait until something is waiting for this member, print it,
// and exit — which is what wakes an idle Claude session that runs it as a background task.
func chatListen() {
	keys := chatSelfKeys("")
	key := ""
	_ = withChatLock(func(r *chatRoom) error {
		if me := r.self(keys); me != nil {
			key = me.Key
		}
		return nil
	})
	if key == "" {
		fmt.Println("[partyline chat] this session is not in the chat; nothing to listen for.")
		return
	}
	_ = withChatLock(func(r *chatRoom) error {
		if m := r.member(key); m != nil {
			m.Active = time.Now()
		}
		return nil
	})
	pidPath := chatListenPidPath(key)
	_ = os.MkdirAll(filepath.Dir(pidPath), 0o700)
	me := strconv.Itoa(os.Getpid())
	_ = os.WriteFile(pidPath, []byte(me), 0o600)
	defer func() {
		if b, _ := os.ReadFile(pidPath); string(b) == me {
			_ = os.Remove(pidPath)
		}
	}()
	// The session that started this: the engine process above the shell it runs in. A listener
	// that outlives its session would take that member's messages and mark them read for nobody,
	// which is what happened in the first test runs.
	session := chatSessionPid()
	for {
		if b, _ := os.ReadFile(pidPath); string(b) != me {
			return // a newer listener for this member took over
		}
		if session > 0 && syscall.Kill(session, 0) != nil {
			return // its session is gone
		}
		text := ""
		joined := true
		_ = withChatLock(func(r *chatRoom) error {
			me := r.member(key)
			if me == nil {
				joined = false
				return nil
			}
			if r.held(*me) {
				return nil
			}
			if msgs := unreadFor(readChat(), *me); len(msgs) > 0 {
				me.Read, me.Answering, me.Sent, me.Active, me.Delivered = msgs[len(msgs)-1].Seq, true, false, time.Now(), time.Now()
				text = chatDeliveryText(*me, msgs, r)
			}
			return nil
		})
		if !joined {
			fmt.Println("[partyline chat] you are no longer in the chat; stop listening.")
			return
		}
		if text != "" {
			fmt.Print(text)
			return
		}
		time.Sleep(time.Second)
	}
}

// engineProgram names the engine a process runs, from its argv: "claude", "codex", or "" for
// anything else. It matches the program by exact name, or the script node runs; a path that merely
// contains "claude" (~/.claude, /tmp/claude-501, a repository called claude-tools) is not an engine.
// The previous substring match took any such path for a Claude session.
func engineProgram(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	prog := filepath.Base(argv[0])
	if prog == "node" && len(argv) > 1 {
		prog = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(filepath.Base(argv[1]), ".js"), ".cjs"), ".mjs")
	}
	if prog == "claude" || prog == "codex" || prog == "gemini" {
		return prog
	}
	return ""
}

// chatSessionPid finds the engine process this listener belongs to: the nearest ancestor whose
// command is claude or codex. Background commands run as engine → shell → ptln.
func chatSessionPid() int {
	pid := os.Getppid()
	for i := 0; i < 6 && pid > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0
		}
		f := strings.Fields(strings.TrimSpace(string(out)))
		if len(f) < 2 {
			return 0
		}
		if engineProgram(f[1:]) != "" {
			return pid
		}
		pid, _ = strconv.Atoi(f[0])
	}
	return 0
}
