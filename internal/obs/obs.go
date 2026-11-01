// Package obs: panic handling for the CLI and the relay.
//
// partyline is self-hosted and has NO error-reporting service. A crash goes to the operator's own
// stderr — the process log they already read — and nowhere else. Nothing here opens a socket, and
// nothing here is configurable, which is the point: a self-hoster should not have to discover that
// their box reports faults to someone else, or find the switch that turns it off.
package obs

import (
	"fmt"
	"os"
)

// Recover re-panics after recovering, preserving the existing behavior (crash + stderr) while
// giving every entry point one place to hang panic handling. Use as: defer obs.Recover().
func Recover() {
	if r := recover(); r != nil {
		panic(r)
	}
}

// Guard recovers a panic in a BACKGROUND goroutine: log it to stderr and CONTINUE — so one bad
// joiner/stream/handler can't crash the whole host process. Use as: defer obs.Guard("serveJoiner").
func Guard(label string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "\r\n[partyline] recovered panic in %s: %v\r\n", label, r)
	}
}
