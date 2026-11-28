package main

import (
	"os"
	"testing"
	"time"
)

func TestRecoveryNoteDoesNotBlockWithoutATTY(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin = r
	if out, e := os.CreateTemp(t.TempDir(), "paint"); e == nil {
		os.Stdout = out
	}
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		recoveryNote("t", []string{"body"})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a modal blocked with no tty on the other end")
	}
}
