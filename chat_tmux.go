package main

// Finding chat members in partyline's tmux, and waking an idle one.
//
// WAKING IS THE ONLY PLACE THE CHAT TYPES INTO A SESSION, and it is the failure the chat exists to
// end: text typed but never submitted, and keystrokes landing in what the person was typing. So a
// wake line is sent only when all of these hold, and otherwise the message waits:
//   - the pane is not the one an attached client is looking at, so the person cannot be typing in it
//   - the session is not working (no "esc to interrupt" on screen)
//   - its input box is empty
// and it is checked afterwards: the box must be empty again, or the wake is reported as failed.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

type chatPane struct {
	ID, Key, Engine, Name, Cwd string
	Window, Index              int
}

// chatPanes lists the agent panes in partyline's tmux: claude or codex running in them.
func chatPanes() []chatPane {
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F",
		"#{pane_id}\t#{window_index}\t#{pane_index}\t#{@ptln_key}\t#{pane_tty}\t#{pane_current_path}").Output()
	if err != nil {
		return nil
	}
	var panes []chatPane
	perWindow := map[int]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		eng := paneEngine(f[4])
		if eng == "" {
			continue
		}
		w, _ := strconv.Atoi(f[1])
		i, _ := strconv.Atoi(f[2])
		key := strings.TrimSpace(f[3])
		if key == "" || isFixtureKey(key) {
			key = "pane:" + f[0]
		}
		panes = append(panes, chatPane{ID: f[0], Key: key, Engine: eng, Window: w, Index: i, Cwd: f[5]})
		perWindow[w]++
	}
	for k := range panes {
		p := &panes[k]
		p.Name = fmt.Sprintf("#%d %s", p.Window, p.Engine)
		if perWindow[p.Window] > 1 {
			p.Name = fmt.Sprintf("#%d.%d %s", p.Window, p.Index, p.Engine)
		}
	}
	return panes
}

// paneEngine reads what runs on a pane's terminal. The pane's own command is not enough: codex
// shows as `node`, and a session started by hand inside a shell shows as the shell.
func paneEngine(tty string) string {
	out, err := exec.Command("ps", "-t", strings.TrimPrefix(tty, "/dev/"), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	s := string(out)
	switch {
	case strings.Contains(s, "claude"):
		return "claude"
	case strings.Contains(s, "codex"):
		return "codex"
	}
	return ""
}

// chatPaneFor finds a pane by the number shown in the tab bar ("11", or "11.2" in a split window),
// or by member key.
func chatPaneFor(panes []chatPane, ref string) (chatPane, bool) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	for _, p := range panes {
		if p.Key == ref || strconv.Itoa(p.Window) == ref || fmt.Sprintf("%d.%d", p.Window, p.Index) == ref {
			return p, true
		}
	}
	return chatPane{}, false
}

// chatSelfPaneKey is the fallback identity for a session with no partyline key: its tmux pane.
// The pane is the one this process runs under, found from its own ancestry, not from $TMUX_PANE:
// codex replays a saved shell environment into the commands it runs, and an old snapshot's
// TMUX_PANE named a pane that no longer existed, so its `ptln chat read` was "not in the chat"
// while the waker kept waking the real pane (2,864 times). The variable is the fallback when
// tmux cannot be asked.
func chatSelfPaneKey() string {
	if p := chatAncestorPane(); p != "" {
		return "pane:" + p
	}
	if p := strings.TrimSpace(os.Getenv("TMUX_PANE")); p != "" {
		return "pane:" + p
	}
	return ""
}

// chatAncestorPane is the pane whose process is this process or one of its ancestors, or "". A
// variable so tests can stand in for tmux and ps.
var chatAncestorPane = func() string {
	out, err := tmuxCmd("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}").Output()
	if err != nil {
		return ""
	}
	panes := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			if pid, err := strconv.Atoi(f[1]); err == nil {
				panes[pid] = f[0]
			}
		}
	}
	return paneOfAncestry(append([]int{os.Getpid()}, processAncestors(os.Getpid())...), panes)
}

// paneOfAncestry is the pane of the nearest process in chain that is a pane's process.
func paneOfAncestry(chain []int, panes map[int]string) string {
	for _, pid := range chain {
		if p, ok := panes[pid]; ok {
			return p
		}
	}
	return ""
}

// paneWatched reports whether an attached client is looking at this pane right now.
func paneWatched(paneID string) bool {
	out, err := tmuxCmd("list-clients", "-F", "#{pane_id}").Output()
	if err != nil {
		return true // cannot tell, so assume the person is there
	}
	// A chat popup covers this pane: the person is reading the room, not that session's input.
	// Only when theirs is the one client: a popup belongs to one client, and a second client
	// may be showing the same pane live.
	if clients := strings.Fields(string(out)); len(clients) == 1 && chatPopupCovers(paneID) {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == paneID {
			return true
		}
	}
	return false
}

// paneIdleEmpty reports whether the session is waiting for input with nothing typed in its box.
// The capture keeps colours (-e): both engines draw their hint text dim, which is the only way to
// tell a hint from text the person typed.
func paneIdleEmpty(paneID string) (bool, string) {
	out, err := tmuxCmd("capture-pane", "-e", "-p", "-t", paneID).Output()
	if err != nil {
		return false, "cannot read the pane"
	}
	// Working and approval are checked on the whole visible screen, so a working line pushed up
	// by a long todo list still counts; the input box is looked for at the bottom. Scrollback is
	// never read: it holds old working lines.
	switch screenActivity(stripColours(string(out))).State {
	case "working":
		return false, "it is working"
	case "approval":
		return false, "it is waiting for your approval"
	}
	return screenIdleEmpty(screenBottom(string(out), 15))
}

// screenIdleEmpty is the screen test, separated for tests. The input line starts with "❯" (Claude)
// or "›" (codex). An empty box shows nothing after the marker, or dim hint text: Claude's suggested
// next prompt or codex's "Ask Codex to do anything". Any visible character after the marker that
// is not dim was typed, and the answer is no.
func screenIdleEmpty(screen string) (bool, string) {
	switch screenActivity(stripColours(screen)).State {
	case "working":
		return false, "it is working"
	case "approval":
		return false, "it is waiting for your approval"
	}
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		plain := strings.Trim(stripColours(lines[i]), " │\t")
		for _, mark := range []string{"❯", "›"} {
			if !strings.HasPrefix(plain, mark) {
				continue
			}
			// "❯ 1. Yes" is a highlighted choice in a menu, not an input box. Enter would pick it.
			if rest := strings.TrimSpace(strings.TrimPrefix(plain, mark)); len(rest) > 1 && rest[0] >= '0' && rest[0] <= '9' && strings.Contains(rest[:min(4, len(rest))], ".") {
				return false, "it is showing a choice"
			}
			if typedAfter(lines[i], mark) {
				return false, "something is typed in its input box"
			}
			return true, ""
		}
	}
	return false, "cannot find its input box"
}

// typedAfter reports whether a raw (coloured) line has non-dim visible text after the marker.
func typedAfter(raw, mark string) bool {
	k := strings.Index(raw, mark)
	if k < 0 {
		return false
	}
	rest, dim := raw[k+len(mark):], false
	for len(rest) > 0 {
		if strings.HasPrefix(rest, "\x1b[") {
			end := strings.IndexByte(rest, 'm')
			if end < 0 {
				return false
			}
			dim = applySGR(dim, rest[2:end])
			rest = rest[end+1:]
			continue
		}
		r, size := utf8.DecodeRuneInString(rest)
		rest = rest[size:]
		if !dim && !unicode.IsSpace(r) && r != '│' {
			return true
		}
	}
	return false
}

// applySGR updates the dim state for one SGR parameter list. The colour forms 38/48/58;5;N and
// 38/48/58;2;R;G;B carry numbers that are not attributes: a "2" in them is a colour value, and
// reading it as dim made truecolor text look like hint text.
func applySGR(dim bool, params string) bool {
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "38", "48", "58":
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		case "2":
			dim = true
		case "0", "", "22":
			dim = false
		}
	}
	return dim
}

// stripColours removes colour codes.
func stripColours(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		if strings.HasPrefix(s, "\x1b[") {
			if end := strings.IndexByte(s, 'm'); end >= 0 {
				s = s[end+1:]
				continue
			}
		}
		b.WriteByte(s[0])
		s = s[1:]
	}
	return b.String()
}

// wakePane types one line into an idle, empty, unwatched pane and submits it, then checks it went.
func wakePane(paneID, line string) error {
	if paneWatched(paneID) {
		return fmt.Errorf("you are looking at it")
	}
	if ok, why := paneIdleEmpty(paneID); !ok {
		return fmt.Errorf("%s", why)
	}
	if err := tmuxCmd("send-keys", "-t", paneID, "-l", line).Run(); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	// Check again before Enter: the person may have switched to this pane in the last 300ms, and
	// Enter would then submit whatever they typed with it.
	if paneWatched(paneID) {
		return fmt.Errorf("you switched to it; the wake line is in its input box, unsent")
	}
	if err := tmuxCmd("send-keys", "-t", paneID, "Enter").Run(); err != nil {
		return err
	}
	time.Sleep(1500 * time.Millisecond)
	// Submitted means the session started working, or its box is empty again. Text still in the
	// box means it did not submit — exactly the failure this file exists to catch.
	out, _ := tmuxCmd("capture-pane", "-e", "-p", "-t", paneID).Output()
	if _, why := screenIdleEmpty(screenBottom(string(out), 15)); why == "something is typed in its input box" {
		// The box was empty before, so what is in it is the wake line: clear it rather than
		// leave it for the person to find.
		if !paneWatched(paneID) {
			_ = tmuxCmd("send-keys", "-t", paneID, "C-u").Run()
		}
		return fmt.Errorf("the line was typed but did not submit")
	}
	return nil
}

// Listener liveness: `ptln chat listen` writes its pid; the room checks it before waking anyone.
func chatListenPidPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(chatDir(), "listen", hex.EncodeToString(sum[:8])+".pid")
}

func chatListening(key string) bool {
	b, err := os.ReadFile(chatListenPidPath(key))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
