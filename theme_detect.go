package main

import (
	"os"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// The outer terminal's background, asked once with OSC 11 before partyline attaches to tmux. A
// popup gets no reply under tmux 3.7c, so nothing inside tmux asks; they read the saved value.

const osc11Timeout = 200 * time.Millisecond

var osc11Reply = regexp.MustCompile(`\x1b\]11;rgba?:([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})`)

// parseOSC11 reads a reply such as "ESC]11;rgb:1b1b/1a1a/1919 BEL" and says whether the colour is
// dark, by the weighted brightness of the encoded channels (not linearised: only the side of 0.5
// matters). Each channel has one to four hex digits, scaled to its own width.
func parseOSC11(reply string) (dark, ok bool) {
	m := osc11Reply.FindStringSubmatch(reply)
	if m == nil {
		return false, false
	}
	var ch [3]float64
	for i, h := range m[1:4] {
		v, _ := strconv.ParseUint(h, 16, 16)
		ch[i] = float64(v) / float64(uint64(1)<<(4*len(h))-1)
	}
	lum := 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
	return lum < 0.5, true
}

// detectBackground asks the terminal and returns "dark" or "light"; when it cannot ask it returns
// previous, and when the terminal does not answer, "none".
func detectBackground(previous string) string {
	if os.Getenv("TMUX") != "" || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return previous
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return previous
	}
	defer tty.Close()
	fd := int(tty.Fd())
	// Only the terminal's foreground job may change its modes; a backgrounded ptln would be stopped
	// by SIGTTOU at MakeRaw.
	if pg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || pg != unix.Getpgrp() {
		return previous
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return previous
	}
	defer term.Restore(fd, state)
	if _, err := tty.WriteString("\x1b]11;?\x07"); err != nil {
		return previous
	}
	// Wait with select rather than block: a read left waiting would swallow every later keystroke.
	// Keys typed inside the 200 ms window itself are read with the reply and dropped; that is the
	// remaining cost. (poll does not work on a tty on macOS: it reports the descriptor invalid.)
	var reply []byte
	deadline := time.Now().Add(osc11Timeout)
	buf := make([]byte, 64)
	for left := time.Until(deadline); left > 0; left = time.Until(deadline) {
		var set unix.FdSet
		set.Set(fd)
		tv := unix.NsecToTimeval(left.Nanoseconds())
		n, err := unix.Select(fd+1, &set, nil, nil, &tv)
		if err != nil || n <= 0 || !set.IsSet(fd) {
			break
		}
		k, err := tty.Read(buf)
		if err != nil || k == 0 {
			break
		}
		reply = append(reply, buf[:k]...)
		if dark, ok := parseOSC11(string(reply)); ok && (reply[len(reply)-1] == 0x07 || len(reply) > 1 && reply[len(reply)-2] == 0x1b) {
			return map[bool]string{true: "dark", false: "light"}[dark]
		}
	}
	return "none"
}
