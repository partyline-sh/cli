//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDesktopUploadControlCCancelsAndPreservesTyping(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	input := &desktopTerminalInput{file: r}
	if _, err := w.Write([]byte("hi\x03")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := input.upload("test-host", source); err == nil {
		t.Fatal("did not cancel")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Ctrl-C blocked on SSH")
	}
	if string(input.queued) != "hi" {
		t.Fatalf("queued %q", input.queued)
	}
}

func TestDesktopUploadConfirmationConsumesReturn(t *testing.T) {
	for _, answer := range []string{"y\r", "\r", "n\r", "\x03"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(answer + "next")); err != nil {
			t.Fatal(err)
		}
		input := &desktopTerminalInput{file: r}
		if got := input.confirm("host", []string{"notes.md"}); got != (answer == "y\r") {
			t.Fatalf("confirmation %q = %v", answer, got)
		}
		var rest [4]byte
		if _, err := input.Read(rest[:]); err != nil || string(rest[:]) != "next" {
			t.Fatalf("confirmation leaked Return: %q, %v", rest, err)
		}
		r.Close()
		w.Close()
	}
}
