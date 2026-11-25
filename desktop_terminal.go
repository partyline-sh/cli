//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Only this reader consumes the local terminal. Keys typed during an upload are
// queued for the draft; Ctrl-C cancels the transfer rather than waiting on SSH.
type desktopTerminalInput struct {
	file   *os.File
	queued []byte
}

func (in *desktopTerminalInput) Read(b []byte) (int, error) {
	if len(in.queued) > 0 {
		n := copy(b, in.queued)
		in.queued = in.queued[n:]
		return n, nil
	}
	return in.file.Read(b)
}

func (in *desktopTerminalInput) confirm(host string, paths []string) bool {
	fmt.Fprintf(os.Stderr, "\r\nUpload %d file(s) to %s?\r\n", len(paths), host)
	for _, path := range paths {
		fmt.Fprintf(os.Stderr, "  %q\r\n", filepath.Base(path))
	}
	fmt.Fprint(os.Stderr, "[y/N] ")
	var answer []byte
	var key [1]byte
	for {
		if _, err := in.file.Read(key[:]); err != nil {
			return false
		}
		switch key[0] {
		case '\r', '\n':
			fmt.Fprint(os.Stderr, "\r\n")
			accepted := strings.EqualFold(string(answer), "y") || strings.EqualFold(string(answer), "yes")
			if !accepted {
				fmt.Fprint(os.Stderr, "Upload cancelled.\r\n")
			}
			return accepted
		case 3, 27:
			fmt.Fprint(os.Stderr, "\r\nUpload cancelled.\r\n")
			return false
		case 127, 8:
			if len(answer) > 0 {
				answer = answer[:len(answer)-1]
				fmt.Fprint(os.Stderr, "\b \b")
			}
		default:
			if key[0] >= 32 && key[0] < 127 && len(answer) < 8 {
				answer = append(answer, key[0])
				fmt.Fprintf(os.Stderr, "%c", key[0])
			}
		}
	}
}

func (in *desktopTerminalInput) upload(host, path string) (string, error) {
	fmt.Fprintf(os.Stderr, "\r\nUploading %q (Ctrl-C cancels)…\r\n", filepath.Base(path))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		path string
		err  error
	}
	finished := make(chan result, 1)
	go func() { path, err := sendDesktopUploadContext(ctx, host, path); finished <- result{path, err} }()
	for {
		select {
		case r := <-finished:
			return r.path, r.err
		default:
		}
		fds := []unix.PollFd{{Fd: int32(in.file.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 50); err != nil {
			if err == unix.EINTR {
				continue
			}
			return "", err
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			return "", errors.New("terminal disconnected; upload cancelled")
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var key [1]byte
		if _, err := in.file.Read(key[:]); err != nil {
			return "", err
		}
		if key[0] == 3 {
			return "", errors.New("upload cancelled")
		}
		if len(in.queued) >= 65536 {
			return "", errors.New("too much queued input; upload cancelled")
		}
		in.queued = append(in.queued, key[0])
	}
}
