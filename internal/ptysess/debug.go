package ptysess

import (
	"fmt"
	"os"
	"sync"
)

// dbg writes a line to /tmp/ptln-debug.log when PTLN_DEBUG is set. Used to trace
// the in-session menu/keystroke path on a real terminal, which the unit tests
// can't reproduce. No-op (and zero cost beyond the env check) otherwise.
var (
	dbgOnce sync.Once
	dbgFile *os.File
)

func dbg(format string, args ...any) {
	dbgOnce.Do(func() {
		if os.Getenv("PTLN_DEBUG") != "" {
			dbgFile, _ = os.OpenFile("/tmp/ptln-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		}
	})
	if dbgFile != nil {
		fmt.Fprintf(dbgFile, format+"\n", args...)
	}
}
