//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestDesktopDropPrefetchedControlCCancelsWithoutLeakingMarkers(t *testing.T) {
	local := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(local, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = w.Write([]byte(pasteStart + local + pasteEnd + "typed\x03after"))
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	input := &desktopTerminalInput{file: r}
	var output bytes.Buffer
	var reported error
	err = forwardDesktopInput(input, &output, func(path string) (string, error) {
		return input.upload("unused-host", path)
	}, func(err error) { reported = err })
	if err != nil || reported == nil || !strings.Contains(reported.Error(), "upload cancelled") {
		t.Fatalf("prefetched cancellation failed: %v, %v", err, reported)
	}
	if output.String() != "typedafter" {
		t.Fatalf("queued input or empty markers leaked: %q", output.String())
	}
}
