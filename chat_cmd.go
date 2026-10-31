package main

// `ptln chat` — the room. The person types here; agents are added by their tab number and answer
// through their own hooks and `ptln chat send` (chat_hook.go). While the room is open it also
// wakes idle members that have messages waiting and no listener (chat_tmux.go).

import (
	"fmt"
	"strings"
	"time"
)

const chatUsage = `usage:
  ptln chat                     open the room: type to talk, /add <tab> to bring a session in
    @11 or @codex addresses a session; a bare @ is text; @all addresses everyone
  ptln chat add <tab>...        add sessions by their tab number (11, or 11.2 in a split)
  ptln chat remove <tab>...     take them out
  ptln chat who                 who is in the room
  ptln chat send "..."          post as this session (what agents run to reply)
  ptln chat read                print what is waiting for this session
  ptln chat listen              wait for a message, print it, exit (Claude runs it in the background)`

func chatMain(args []string) {
	ensureWatcher()
	if len(args) == 0 {
		// Inside partyline's tmux the room opens as a popup over the tab you are in; anywhere else
		// it runs in this terminal.
		if insidePtlnTmux() {
			if err := tmuxCmd(chatPopupCmd(selfExe())...).Run(); err != nil {
				fatal(err)
			}
			return
		}
		chatPopupMain(false)
		return
	}
	switch args[0] {
	case "--here": // what the popup runs
		chatPopupMain(false)
		return
	case "--tab":
		chatPopupMain(true)
		return
	case "add":
		fmt.Println(chatAdd(args[1:]))
	case "remove", "rm":
		fmt.Println(chatRemove(args[1:]))
	case "who":
		fmt.Print(chatWho())
	case "send":
		chatSendSelf(strings.Join(args[1:], " "))
	case "read":
		chatReadSelf()
	case "listen":
		chatListen()
	case "-h", "--help", "help":
		fmt.Println(chatUsage)
	default:
		fatal(fmt.Errorf("ptln chat: unknown %q\n%s", args[0], chatUsage))
	}
}

// chatAdd brings sessions in by tab number. A new member is handed the last few messages so it
// joins the conversation rather than its next line.
func chatAdd(refs []string) string {
	if len(refs) == 0 {
		return "add which session? give its tab number, e.g. /add 11"
	}
	panes := chatPanes()
	var said []string
	_ = withChatLock(func(r *chatRoom) error {
		last := 0
		if msgs := readChat(); len(msgs) > 0 {
			last = msgs[len(msgs)-1].Seq
		}
		for _, ref := range refs {
			p, ok := chatPaneFor(panes, ref)
			if !ok {
				said = append(said, fmt.Sprintf("no claude or codex session in tab %s", ref))
				continue
			}
			if r.member(p.Key) != nil {
				said = append(said, p.Name+" is already here")
				continue
			}
			r.Members = append(r.Members, chatMember{Key: p.Key, Name: p.Name, Engine: p.Engine, Read: max(0, last-20), Joined: time.Now()})
			_, _ = postLocked(r, chatMessage{From: "partyline", Name: "partyline", Text: p.Name + " joined", Note: true})
			said = append(said, p.Name+" added")
		}
		return nil
	})
	return strings.Join(said, "\n")
}

func chatRemove(refs []string) string {
	var said []string
	_ = withChatLock(func(r *chatRoom) error {
		for _, ref := range refs {
			ref = strings.TrimPrefix(ref, "#")
			kept := r.Members[:0]
			for _, m := range r.Members {
				num := strings.TrimPrefix(strings.Fields(m.Name)[0], "#")
				if num == ref || m.Key == ref {
					_, _ = postLocked(r, chatMessage{From: "partyline", Name: "partyline", Text: m.Name + " left", Note: true})
					said = append(said, m.Name+" removed")
					continue
				}
				kept = append(kept, m)
			}
			r.Members = kept
		}
		return nil
	})
	if len(said) == 0 {
		return "nobody matched"
	}
	return strings.Join(said, "\n")
}

func chatWho() string {
	var b strings.Builder
	_ = withChatLock(func(r *chatRoom) error {
		if len(r.Members) == 0 {
			b.WriteString("nobody yet — /add <tab> brings a session in\n")
		}
		for _, m := range r.Members {
			state := "idle, no listener"
			if chatListening(m.Key) {
				state = "listening"
			}
			if m.Engine == "codex" {
				state = "woken when a message arrives"
			}
			if m.Paused {
				state += "; delivery stopped (/go " + strings.TrimPrefix(strings.Fields(m.Name)[0], "#") + ")"
			}
			fmt.Fprintf(&b, "%-14s %s\n", m.Name, state)
		}
		switch {
		case r.Stopped:
			b.WriteString("delivery is stopped (/stop); /go or your next message resumes it\n")
		case r.paused():
			fmt.Fprintf(&b, "the sessions posted %d messages in a row; they wait for you to say something\n", r.AgentRun)
		}
		return nil
	})
	return b.String()
}

// chatStop pauses delivery (stop=true) or resumes it, for the whole room when no tabs are given,
// otherwise for those members.
func chatStop(refs []string, stop bool) string {
	var said []string
	_ = withChatLock(func(r *chatRoom) error {
		if len(refs) == 0 {
			r.Stopped = stop
			for i := range r.Members {
				if !stop {
					r.Members[i].Paused = false
				}
			}
			if stop {
				said = append(said, "delivery to every session stopped; /go or your next message resumes it")
			} else {
				r.AgentRun = 0
				said = append(said, "delivery resumed")
			}
			return nil
		}
		for _, ref := range refs {
			ref = strings.TrimPrefix(ref, "#")
			found := false
			for i := range r.Members {
				m := &r.Members[i]
				if strings.TrimPrefix(strings.Fields(m.Name)[0], "#") == ref || m.Key == ref {
					m.Paused, found = stop, true
					if stop {
						said = append(said, m.Name+": delivery stopped until /go "+ref)
					} else {
						said = append(said, m.Name+": delivery resumed")
					}
				}
			}
			if !found {
				said = append(said, "nobody in the room is in tab "+ref)
			}
		}
		return nil
	})
	return strings.Join(said, "\n")
}

// chatSendSelf posts as the calling session. Run by agents.
func chatSendSelf(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		fatal(fmt.Errorf("usage: ptln chat send \"message\""))
	}
	keys := chatSelfKeys("")
	err := withChatLock(func(r *chatRoom) error {
		me := r.self(keys)
		if me == nil {
			return fmt.Errorf("this session is not in the chat")
		}
		me.Sent, me.Active = true, time.Now()
		_, err := postLocked(r, chatMessage{From: me.Key, Name: me.Name, To: chatAddressees(r, text), Text: text})
		return err
	})
	if err != nil {
		fatal(err)
	}
	fmt.Println("sent")
}

func chatReadSelf() {
	keys := chatSelfKeys("")
	text := ""
	_ = withChatLock(func(r *chatRoom) error {
		me := r.self(keys)
		if me == nil {
			text = "this session is not in the chat\n"
			return nil
		}
		if msgs := unreadFor(readChat(), *me); len(msgs) > 0 && !r.held(*me) {
			me.Read, me.Answering, me.Sent, me.Active, me.Delivered = msgs[len(msgs)-1].Seq, true, false, time.Now(), time.Now()
			text = chatDeliveryText(*me, msgs, r)
		} else {
			text = "nothing new in the chat\n"
		}
		return nil
	})
	fmt.Print(text)
}

// chatWakeQuiet is how long after a member last received, sent or started listening before the
// room will type a wake line into it. In the first interactive test a message landed in the second
// between a listener finishing and the next one starting, and the wake was typed into a session
// that was busy replying.
const chatWakeQuiet = time.Minute

// chatWakeLine is all that is ever typed into a session: a pointer to `ptln chat read`.
const chatWakeLine = "[partyline chat] You have new messages in the partyline chat. Run `ptln chat read` and follow it."

// chatWakeIdle wakes members that have messages waiting and nothing listening. wake records, per
// member, why the last wake was held back ("" once it went in), for the room's status lines; a
// member with nothing waiting is dropped from it.
func chatWakeIdle(lastTry map[string]time.Time, setWake func(key, why string, drop bool)) {
	type want struct {
		key, name string
		read      int
	}
	var due []want
	_ = withChatLock(func(r *chatRoom) error {
		msgs := readChat()
		for _, m := range r.Members {
			if len(unreadFor(msgs, m)) == 0 {
				setWake(m.Key, "", true)
				continue
			}
			if r.held(m) || (m.Engine == "claude" && chatListening(m.Key)) {
				continue
			}
			quiet := chatWakeQuiet
			if m.Engine == "codex" {
				quiet = 10 * time.Second // no listener gap to cover; its "Working (" line shows it is busy
			}
			if time.Since(m.Active) < quiet {
				continue // mid-conversation: its listener or its turn will pick this up
			}
			due = append(due, want{m.Key, m.Name, m.Read})
		}
		return nil
	})
	if len(due) == 0 {
		return
	}
	panes := chatPanesNow()
	for _, d := range due {
		if time.Since(lastTry[d.key]) < 20*time.Second {
			continue
		}
		// A member woken this many times whose read mark never moved is not reading: each wake
		// costs it a model turn for nothing. It is left alone, and the room says why, until it reads.
		tries := chatWakeTries[d.key]
		if tries.read != d.read {
			tries = chatTries{read: d.read}
		}
		if tries.n >= chatWakeMax {
			setWake(d.key, fmt.Sprintf("woken %d times without reading: it may not be able to run `ptln chat read`", tries.n), false)
			continue
		}
		lastTry[d.key] = time.Now()
		p, ok := chatPaneFor(panes, d.key)
		switch {
		case !ok:
			setWake(d.key, "its tab is gone", false)
		default:
			if err := wakePaneNow(p.ID, chatWakeLine); err != nil {
				setWake(d.key, err.Error(), false)
			} else {
				tries.n++
				setWake(d.key, "", false)
			}
		}
		chatWakeTries[d.key] = tries
	}
}

// chatPanesNow and wakePaneNow are the waker's view of tmux; variables so tests can stand in.
var chatPanesNow, wakePaneNow = chatPanes, wakePane

// chatWakeMax is how many wakes a member gets without its read mark moving.
const chatWakeMax = 3

// chatTries counts wakes since a member's read mark was last seen at read. The watcher's tick is
// its only user.
type chatTries struct{ read, n int }

var chatWakeTries = map[string]chatTries{}
