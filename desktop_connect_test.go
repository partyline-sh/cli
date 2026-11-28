//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// The fixture replaces only SSH transport. Connect's real PTY/input loop and the
// actual upload receiver still run in child processes.
func TestDesktopConnectFixture(t *testing.T) {
	if os.Getenv("PARTYLINE_DESKTOP_CONNECT_FIXTURE") != "1" {
		return
	}
	root := os.Getenv("PARTYLINE_DESKTOP_CONNECT_ROOT")
	mode := "client"
	for _, arg := range os.Args {
		if arg == "-tt" {
			mode = "terminal"
		} else if arg == "-T" {
			mode = "upload"
		}
	}
	if mode == "client" {
		if err := desktopConnect([]string{"fixture-host"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if mode == "upload" {
		if os.Getenv("PARTYLINE_DESKTOP_CONNECT_FAIL") == "1" {
			os.Exit(1)
		}
		path, err := receiveDesktopUpload(filepath.Join(root, "remote"), os.Stdin, time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"path": path})
		os.Exit(0)
	}
	if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
		os.Exit(1)
	}
	capture, err := os.OpenFile(filepath.Join(root, "received"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	fmt.Fprint(os.Stdout, pasteOn)
	_ = os.WriteFile(filepath.Join(root, "ready"), nil, 0o600)
	var b [4096]byte
	for {
		n, err := os.Stdin.Read(b[:])
		if stop := bytes.IndexByte(b[:n], 4); stop >= 0 {
			_, _ = capture.Write(b[:stop])
			os.Exit(0)
		}
		_, _ = capture.Write(b[:n])
		if err != nil {
			os.Exit(0)
		}
	}
}

func TestDesktopConnectPTYDropWithoutPrompt(t *testing.T) {
	for _, scenario := range []string{"screenshot", "refused", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			name := "Screenshot 2026-11-25 at 9.20.51\u202fAM.png"
			if scenario == "refused" {
				name = ".env"
			}
			local := filepath.Join(root, name)
			if err := os.WriteFile(local, []byte("fixture screenshot"), 0o600); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			_ = os.Mkdir(bin, 0o700)
			script := "#!/bin/sh\nexec " + quoteDesktopPath(exe) + " -test.run=TestDesktopConnectFixture -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=TestDesktopConnectFixture")
			cmd.Env = append(os.Environ(), "PARTYLINE_DESKTOP_CONNECT_FIXTURE=1", "PARTYLINE_DESKTOP_CONNECT_ROOT="+root, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if scenario == "failed" {
				cmd.Env = append(cmd.Env, "PARTYLINE_DESKTOP_CONNECT_FAIL=1")
			}
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer cmd.Process.Kill()
			var screen bytes.Buffer
			copied := make(chan struct{})
			go func() { _, _ = io.Copy(&screen, terminal); close(copied) }()
			waitFile := func(name string, ready func([]byte) bool) []byte {
				t.Helper()
				deadline := time.Now().Add(8 * time.Second)
				for time.Now().Before(deadline) {
					if data, err := os.ReadFile(filepath.Join(root, name)); err == nil && ready(data) {
						return data
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("timed out waiting for fixture %s", name)
				return nil
			}
			waitFile("ready", func([]byte) bool { return true })
			// No y/n answer is provided. Input following the drop is in the same
			// terminal read and must reach the remote draft after upload completes.
			_, _ = io.WriteString(terminal, "draft "+pasteStart+strings.ReplaceAll(local, " ", "\\ ")+pasteEnd+" tail")
			received := waitFile("received", func(b []byte) bool { return bytes.HasSuffix(b, []byte(" tail")) })
			chat := newChatInput(nil)
			result := chat.handle(received)
			if len(result.send) != 0 || chat.pasting || strings.Contains(chat.ed.String(), "[200~") {
				t.Fatalf("control markers or automatic send: %q", chat.ed.String())
			}
			files, _ := filepath.Glob(filepath.Join(root, "remote", "desktop", "*", name))
			if scenario == "screenshot" {
				if len(files) != 1 || chat.ed.String() != "draft "+quoteDesktopPath(files[0])+" tail" {
					t.Fatalf("upload/draft failed: %q, files=%d", chat.ed.String(), len(files))
				}
				data, _ := os.ReadFile(files[0])
				if string(data) != "fixture screenshot" {
					t.Fatal("uploaded bytes differ")
				}
			} else if len(files) != 0 || string(received) != "draft  tail" {
				t.Fatalf("rejected upload leaked bytes: %q", received)
			}
			_, _ = terminal.Write([]byte{4})
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			<-copied
			if strings.Contains(screen.String(), "[y/N]") {
				t.Fatal("unexpected upload confirmation")
			}
		})
	}
}
