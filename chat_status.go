package main

// The live line per session above the input: where your message is and what each session is
// doing with it. Without it, nothing on screen changed between sending a message and a reply
// arriving, which could be a minute.

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

var chatVerbs = []string{
	"thinking", "pondering", "mulling", "tinkering", "noodling", "chewing on it", "puzzling",
	"working it out", "sketching", "weighing it", "digging in", "untangling", "cooking",
	"brewing", "reasoning", "considering", "drafting", "checking", "piecing it together",
	"turning it over", "crunching", "sifting", "musing", "scheming", "assembling",
}

const chatSpinner = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// chatVerb picks a verb for one turn: the same for the whole turn, different from turn to turn.
func chatVerb(key string, turn int) string {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s/%d", key, turn)
	return chatVerbs[int(h.Sum32())%len(chatVerbs)]
}

func chatSpinFrame(now time.Time) string {
	frames := []rune(chatSpinner)
	return string(frames[int(now.UnixMilli()/100)%len(frames)])
}

// chatStatusLines describes every member that has something going on. wake holds the last reason
// a wake was held back for a member ("" once woken); act holds what each member is doing in its own
// tab (chat_activity.go). The second result is true while any line is animated.
//
// One line per member, by priority: waiting for your approval, working on a chat message, working
// in its tab, a message on its way, idle with messages waiting, just replied. Lines show during a
// pause too: a pause stops delivery, not the sessions.
func chatStatusLines(r *chatRoom, msgs []chatMessage, wake map[string]string, act map[string]memberActivity, now time.Time) ([]string, bool) {
	var lines []string
	spinning := false
	spin := chatSpinFrame(now)
	for _, m := range r.Members {
		a := act[m.Key]
		tab := strings.TrimPrefix(strings.Fields(m.Name + " ")[0], "#")
		doing := ""
		if a.Doing != "" {
			doing = " · " + a.Doing
		}
		unread := len(unreadFor(msgs, m))
		waiting := unread > 0
		switch {
		case a.State == "approval":
			lines = append(lines, fmt.Sprintf("‼ %s · waiting for your approval in tab %s%s", m.Name, tab, doing))
		case m.Answering && !m.Sent && (a.State == "working" || now.Sub(m.Delivered) < chatAnswerGrace):
			spinning = true
			lines = append(lines, fmt.Sprintf("%s %s · %s… %s%s", spin, m.Name, chatVerb(m.Key, m.Read), chatElapsed(now.Sub(m.Delivered)), doing))
		case a.State == "working":
			spinning = true
			own := strings.TrimSpace(a.Verb + " " + a.Elapsed)
			if own != "" {
				own = " · " + own
			}
			extra := ""
			if waiting {
				extra = fmt.Sprintf(" · %d message%s waiting", unread, plural(unread, "", "s"))
			}
			lines = append(lines, fmt.Sprintf("%s %s · working in its tab%s%s%s", spin, m.Name, own, doing, extra))
		case waiting && m.Paused && !r.paused():
			lines = append(lines, fmt.Sprintf("· %s · idle, %d message%s waiting · stopped: /go %s delivers them", m.Name, unread, plural(unread, "", "s"), tab))
		case waiting && r.paused():
			lines = append(lines, fmt.Sprintf("· %s · idle, %d message%s waiting (delivery paused)", m.Name, unread, plural(unread, "", "s")))
		case waiting && chatListening(m.Key):
			spinning = true
			lines = append(lines, fmt.Sprintf("%s %s · receiving", spin, m.Name))
		case waiting:
			why, tried := wake[m.Key]
			switch {
			case tried && why != "":
				lines = append(lines, fmt.Sprintf("· %s · idle, %d message%s waiting: %s", m.Name, unread, plural(unread, "", "s"), why))
			case tried:
				spinning = true
				lines = append(lines, fmt.Sprintf("%s %s · waking up", spin, m.Name))
			default:
				spinning = true
				lines = append(lines, fmt.Sprintf("%s %s · gets it when its current turn ends", spin, m.Name))
			}
		case m.Sent && now.Sub(m.Active) < 4*time.Second:
			lines = append(lines, fmt.Sprintf("✓ %s · replied", m.Name))
		}
	}
	switch {
	case r.Stopped:
		lines = append(lines, "· delivery stopped (/stop): /go or your next message resumes it")
	case r.paused():
		lines = append(lines, fmt.Sprintf("· the sessions posted %d messages in a row; they wait for you to say something", r.AgentRun))
	}
	return lines, spinning
}

// chatAnswerGrace is how long a delivered message counts as being worked on before the session's
// screen has to show it working. The flag behind that line is cleared by the session's Stop hook,
// and sessions added to the room by tab number may have been started without one: a member that
// read a message and chose not to reply then showed "weighing it… 763m" overnight.
const chatAnswerGrace = 30 * time.Second

func chatElapsed(d time.Duration) string {
	if d < 0 || d > 24*time.Hour {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
