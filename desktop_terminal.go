//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

func (in *desktopTerminalInput) unread(b []byte) error {
	if len(b)+len(in.queued) > 65536 {
		return errors.New("too much queued input; upload cancelled")
	}
	copyOfInput := append([]byte(nil), b...)
	in.queued = append(copyOfInput, in.queued...)
	return nil
}

func (in *desktopTerminalInput) upload(host, path string) (string, error) {
	if i := bytes.IndexByte(in.queued, 3); i >= 0 {
		in.queued = append(in.queued[:i], in.queued[i+1:]...)
		return "", errors.New("upload cancelled")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "\r\nUploading %q (%d bytes; Ctrl-C cancels)…\r\n", filepath.Base(path), info.Size())
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
