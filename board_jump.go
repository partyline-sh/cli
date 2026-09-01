package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"partyline.sh/partyline/internal/api"
)

// board_jump.go — the moves the web board structurally cannot make.
//
// This is the payoff for putting the board INSIDE the terminal rather than in a separate window: a
// tile is a few keystrokes from the thing it describes. `s` attaches the run's live session, `r`
// reads its diff, `o` opens its PR, and a run that surfaced a dev server offers its URL. The web
// board can show you a log; this one can put your hands on the terminal the log came from.

// attachSession opens the focused run's live session in a new mux window.
//
// Returns whether the board should EXIT. Inside the ptln tmux the board is one window among many,
// so attaching opens another window and the board stays where it is. Outside tmux there is no
// window to open, so the board hands the terminal over and gets out of the way — the alternative
// (refusing) would make the feature exist only for tmux users, and the alternative to that
// (spawning a bare shell over the board) would leave two programs fighting for one screen.
func (m *boardModel) attachSession() bool {
	card, ok := m.focused()
	if !ok {
		return false
	}
	if card.Machine == "" {
		m.setToast("this card has no machine — nothing to attach to", true)
		return false
	}
	if !isThisMachine(card.Machine) {
		m.setToast("that run is on "+card.Machine+", not this machine — open it there", true)
		return false
	}

	target := runSessionTarget(card.ID)
	if target == "" {
		m.setToast("no live session for this run on this machine", true)
		return false
	}

	if insidePtlnTmux() {
		if err := tmuxCmd("select-window", "-t", target).Run(); err != nil {
			m.setToast("could not switch to that session: "+err.Error(), true)
			return false
		}
		return false
	}

	// Outside tmux there is no window to switch to, so the board hands the terminal over: it
	// restores the tty and EXECs the attach in place. See boardExec for why exec rather than spawn.
	m.handOff = func() {
		_ = tmuxCmd("select-window", "-t", target).Run()
		boardExec(tmuxCmd("attach-session", "-t", tmuxSessionName))
	}
	return true
}

// isThisMachine reports whether a board card's machine label names the box we are on. The label is
// the daemon's device name, which is this machine's hostname.
func isThisMachine(label string) bool {
	if label == "" {
		return false
	}
	host, err := os.Hostname()
	if err != nil {
		return false
	}
	// Compare on the first dot-separated segment: the fleet shows "MacBook-Air.local" and the
	// hostname may or may not carry the domain depending on how the box is configured.
	return strings.EqualFold(shortHost(host), shortHost(label))
}

func shortHost(s string) string {
	if i := strings.Index(s, "."); i > 0 {
		return s[:i]
	}
	return s
}

// runSessionTarget finds the tmux window running this run, if any. crank names its windows after
// the run, which is what makes the tile→session jump possible at all.
func runSessionTarget(runID string) string {
	if runID == "" {
		return ""
	}
	// Deliberately NOT gated on insidePtlnTmux: the tmux server holds the run's window whether or
	// not THIS process is attached to it, and gating here made the whole outside-tmux hand-off
	// unreachable — `s` from a plain terminal always refused, on the very machine that owned the run.
	out, err := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}\t#{window_name}").Output()
	if err != nil {
		return ""
	}
	short := runID
	if len(short) > 8 {
		short = short[:8]
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) != 2 {
			continue
		}
		if strings.Contains(f[1], short) {
			return f[0]
		}
	}
	return ""
}

// reviewDiff opens the run's diff for reading — the terminal half of the review gate.
//
// It prefers the PR (that is the artifact a reviewer signs off on) and falls back to the local
// worktree branch when the run never opened one, because a branch-only run is exactly the case
// where a human has to look at the code and the web board can only tell them there is no PR.
func (m *boardModel) reviewDiff() {
	card, ok := m.focused()
	if !ok {
		return
	}
	if card.PRURL != "" {
		m.openURL(card.PRURL, "PR")
		return
	}
	if card.NoPR != nil {
		m.openOverlay(&noticeOverlay{heading: "no PR to review", body: wrapPlain(
			noPRExplanation(card.NoPR.Kind, card.NoPR.Detail), 70)})
		return
	}
	m.setToast("this run has no PR yet", false)
}

// noPRExplanation turns the three no-PR kinds into what the reader should DO, which is the only
// reason to distinguish them.
func noPRExplanation(kind, detail string) string {
	switch kind {
	case "branch-only":
		return "This run pushed a branch but never opened a PR — its merge policy is manual. " +
			"Merge the branch yourself when it looks right.\n\n" + detail
	case "pr-failed":
		return "The push worked but opening the PR did not. Open it from the run page, or push it " +
			"again from the machine.\n\n" + detail
	case "no-changes":
		return "Nothing was committed, so there is nothing to review. Some tasks produce no code " +
			"by nature; if this one should have, read the run log for what it did instead.\n\n" + detail
	}
	return detail
}

// openPR opens the focused card's pull request.
func (m *boardModel) openPR() {
	card, ok := m.focused()
	if !ok {
		return
	}
	if card.PRURL == "" {
		m.setToast("this card has no PR", false)
		return
	}
	m.openURL(card.PRURL, "PR")
}

// openURL hands a URL to the desktop, reporting what it did. It never opens anything the CARD did
// not carry: the only URLs that reach here come from the control plane's own fields.
func (m *boardModel) openURL(u, what string) {
	if strings.TrimSpace(u) == "" {
		m.setToast("no "+what+" to open", false)
		return
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		m.setToast("refusing to open a non-http URL", true)
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		m.setToast("could not open the "+what+": "+u, true)
		return
	}
	m.setToast("opened the "+what+" in your browser", false)
}

// previewURLRe finds a dev-server URL a run announced in its output. Deliberately narrow: only
// localhost and 127.0.0.1 with a port, which is what a dev server prints and what is safe to offer
// as something to click. Anything else in a log line is just text.
var previewURLRe = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1)(?::\d{2,5})?(?:/\S*)?`)

// previewURL is the dev server this run brought up, if it said so in its last line. A run that
// starts a preview and never tells you the URL is a run you have to go digging for.
func previewURL(c api.BoardCard) string {
	for _, s := range []string{c.LastLine, c.Detail} {
		if u := previewURLRe.FindString(s); u != "" {
			return u
		}
	}
	return ""
}

// boardBell rings the terminal when a card arrives somewhere that wants a human, and names what
// arrived. Silent otherwise — a board that beeps at every state change is a board people mute.
//
// It compares against the PREVIOUS board rather than a timestamp: "moved into a column that needs
// me" is the event, and a card that was already blocked when the board opened is not news.
func boardBell(prev, next *api.Board) (ring bool, note string) {
	if prev == nil || next == nil {
		return false, ""
	}
	was := map[string]api.BoardColumn{}
	for _, col := range api.BoardColumns {
		for _, c := range prev.Column(col) {
			was[c.ID] = col
		}
	}
	arrived := 0
	first := ""
	for _, col := range []api.BoardColumn{api.ColReview, api.ColBlocked} {
		for _, c := range next.Column(col) {
			if old, seen := was[c.ID]; !seen || old != col {
				arrived++
				if first == "" {
					first = col.Title() + ": " + cardTitle(c)
				}
			}
		}
	}
	switch arrived {
	case 0:
		return false, ""
	case 1:
		return true, first
	}
	return true, fmt.Sprintf("%s (+%d more)", first, arrived-1)
}

// boardExec replaces this process with cmd, so the program the operator was handed owns the
// terminal outright — one process, one stdin reader, no race with the board's own.
//
// Falls back to running it as a child if exec fails (a resolvable-but-not-executable path, say):
// a working hand-off with a keystroke race is better than none at all, and the fallback is the
// unusual case rather than the normal one.
func boardExec(cmd *exec.Cmd) {
	path, err := exec.LookPath(cmd.Path)
	if err != nil {
		path = cmd.Path
	}
	if err := syscall.Exec(path, cmd.Args, os.Environ()); err != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		_ = cmd.Run()
	}
}
