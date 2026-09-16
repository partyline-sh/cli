package ptymux

import (
	"fmt"
	"os"
	"strings"
)

// inputDebug logs raw + normalized stdin chunks to a file when PTLN_MUX_DEBUG is set,
// for diagnosing how a terminal/app encodes keys (e.g. ctrl-\ inside a full-screen TUI).
// Off (nil writes) unless the env var points at a writable path.
type inputDebug struct{ f *os.File }

func newInputDebug() *inputDebug {
	path := strings.TrimSpace(os.Getenv("PTLN_MUX_DEBUG"))
	if path == "" {
		return &inputDebug{}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &inputDebug{}
	}
	fmt.Fprintf(f, "--- ptln mux input log ---\n")
	return &inputDebug{f: f}
}

func (d *inputDebug) log(mode string, raw, norm []byte) {
	if d == nil || d.f == nil {
		return
	}
	if string(raw) == string(norm) {
		fmt.Fprintf(d.f, "[%s] %s\n", mode, hexDump(raw))
	} else {
		fmt.Fprintf(d.f, "[%s] raw=%s  norm=%s\n", mode, hexDump(raw), hexDump(norm))
	}
}

// hexDump renders bytes as space-separated hex plus a printable-ASCII gloss.
func hexDump(b []byte) string {
	var hx, asc strings.Builder
	for i, c := range b {
		if i > 0 {
			hx.WriteByte(' ')
		}
		fmt.Fprintf(&hx, "%02x", c)
		if c >= 0x20 && c < 0x7f {
			asc.WriteByte(c)
		} else {
			asc.WriteByte('.')
		}
	}
	return hx.String() + "  |" + asc.String() + "|"
}
