//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func desktopConnectMain(args []string) {
	if err := desktopConnect(args); err != nil {
		fatal(err)
	}
}

func desktopConnect(args []string) error {
	if len(args) != 1 || !desktopSSHHost.MatchString(args[0]) {
		return errors.New("usage: ptln connect <SSH-host>")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("ptln connect needs an interactive terminal")
	}
	cmd := exec.Command("ssh", "-tt", "--", args[0], "ptln")
	master, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer master.Close()
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), state)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	done := make(chan struct{})
	defer close(done)
	go func() {
		_ = pty.InheritSize(os.Stdin, master)
		for {
			select {
			case <-resize:
				_ = pty.InheritSize(os.Stdin, master)
			case <-done:
				return
			}
		}
	}()
	go func() {
		input := &desktopTerminalInput{file: os.Stdin}
		err := forwardDesktopInput(input, master, func(path string) (string, error) { return input.upload(args[0], path) }, func(err error) { fmt.Fprintf(os.Stderr, "\r\n%s\r\n", err) })
		if err != nil {
			fmt.Fprintf(os.Stderr, "\r\n%s\r\n", err)
			_ = cmd.Process.Signal(syscall.SIGHUP)
		}
	}()
	_, _ = io.Copy(os.Stdout, master)
	return cmd.Wait()
}
