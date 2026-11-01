// Package artifactlock serializes the tests that MUTATE generated artifacts in the working
// tree against the tests that READ them.
//
// Two packages check the same files. internal/surfacegen proves the drift gate notices a hand
// edit, which it can only do by hand-editing a real artifact and putting it back; the root
// package asserts web/public/llms-full.txt matches the generator byte for byte. `go test ./...`
// runs packages in PARALLEL, so the reader could land inside the mutator's window and fail with
// "stale vs the generator" on a tree that was never stale. It was intermittent, it was blamed on
// the tree more than once, and it eventually failed a release gate.
//
// A lock file rather than a mutex: the contenders are separate processes, so nothing in-process
// can coordinate them. O_EXCL rather than syscall.Flock to keep it portable.
package artifactlock

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// staleAfter: a lock older than this belonged to a test binary that was killed. Long enough that
// no healthy holder is ever evicted (these critical sections are two Check() calls), short enough
// that a crashed run does not wedge the next one.
const staleAfter = 2 * time.Minute

// Acquire blocks until it holds the artifact lock, then returns the release function. On any
// filesystem trouble it returns a no-op release rather than failing a test for an unrelated
// reason: the worst case is the flake it was written to prevent, which is where we started.
func Acquire() func() {
	path := filepath.Join(os.TempDir(), "partyline-artifact-tests.lock")
	deadline := time.Now().Add(staleAfter + 30*time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }
		}
		if !errors.Is(err, os.ErrExist) {
			return func() {}
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > staleAfter {
			_ = os.Remove(path) // the holder died; take it over
			continue
		}
		if time.Now().After(deadline) {
			return func() {}
		}
		time.Sleep(25 * time.Millisecond)
	}
}
