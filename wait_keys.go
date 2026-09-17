package main

import (
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// The stdin half of waitJob, kept apart from the loop it feeds: this is the part that has to know
// about raw mode and terminal bytes, and it is the part the old ask_peer wait simply didn't have.

// waitKeys puts stdin in raw mode and streams decoded keypresses until stop() is called. It POLLS
// with a short timeout rather than parking in a blocking Read, so stop() can't leave a goroutine
// holding a keystroke that the next cooked-mode prompt was supposed to get. stop() waits for the
// reader to exit before restoring the tty, so the caller's next prompt is never racing raw mode.
// With no tty it returns an empty channel and a no-op stop — nothing here can hang a pipe.
func waitKeys() (<-chan rune, func()) {
	ch := make(chan rune, 4)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		close(ch)
		return ch, func() {}
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		close(ch)
		return ch, func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var buf [8]byte
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, perr := unix.Poll(fds, 100)
			if perr == unix.EINTR {
				continue
			}
			if perr != nil {
				return
			}
			if n == 0 {
				continue
			}
			rn, rerr := os.Stdin.Read(buf[:])
			if rerr != nil || rn == 0 {
				return
			}
			k, ok := decodeKey(buf[:rn])
			if !ok {
				continue
			}
			select {
			case ch <- k:
			case <-stop:
				return
			}
		}
	}()
	return ch, func() {
		close(stop)
		<-done
		_ = term.Restore(fd, old)
	}
}

// decodeKey maps one raw read to a key using menuKey's rules (menu_box.go), so a wait and a menu
// agree on what cancels: lone esc / ctrl-c / ctrl-\ → 0 (CANCEL), esc+more bytes → an arrow or
// function key (ok=false, ignore), enter → '\n', letters folded to lower case.
func decodeKey(b []byte) (rune, bool) {
	if len(b) == 0 {
		return 0, false
	}
	switch b[0] {
	case 0x1b:
		return 0, len(b) == 1
	case 0x03, 0x1c:
		return 0, true
	case '\r', '\n':
		return '\n', true
	}
	r := rune(b[0])
	if r >= 'A' && r <= 'Z' {
		r += 'a' - 'A'
	}
	return r, true
}
