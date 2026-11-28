package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type desktopChunks struct{ chunks [][]byte }

func (r *desktopChunks) Read(b []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(b, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestDesktopDropMacScreenshotUnicodeAndSplitMarkers(t *testing.T) {
	local := filepath.Join(t.TempDir(), "Screenshot 2026-11-25 at 9.20.51\u202fAM.png")
	if err := os.WriteFile(local, []byte("screenshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	// macOS escapes ASCII spaces, but leaves the narrow no-break space intact.
	input := pasteStart + strings.ReplaceAll(local, " ", "\\ ") + pasteEnd
	for split := 1; split < len(input); split++ {
		reader := &desktopChunks{chunks: [][]byte{[]byte(input[:split]), []byte(input[split:])}}
		var output bytes.Buffer
		calls := 0
		err := forwardDesktopInput(reader, &output, func(path string) (string, error) {
			if path != local {
				t.Fatalf("wrong local path at split %d: %q", split, path)
			}
			calls++
			return "/host/screenshot.png", nil
		}, func(err error) { t.Fatal(err) })
		if err != nil || calls != 1 {
			t.Fatalf("split %d: uploads=%d error=%v", split, calls, err)
		}
		chat := newChatInput(nil)
		for _, b := range output.Bytes() {
			if result := chat.handle([]byte{b}); len(result.send) != 0 {
				t.Fatal("upload submitted the draft")
			}
		}
		if chat.ed.String() != "'/host/screenshot.png'" || chat.pasting {
			t.Fatalf("split %d leaked a paste marker: %q", split, chat.ed.String())
		}
	}
}

func TestDesktopDropSymlinkCannotBypassSensitivePathFilter(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, ".env")
	if err := os.WriteFile(secret, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "ordinary.txt")
	if err := os.Symlink(secret, alias); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	reported := false
	err := forwardDesktopInput(strings.NewReader(pasteStart+alias+pasteEnd), &output,
		func(string) (string, error) { t.Fatal("uploaded a hidden symlink target"); return "", nil },
		func(error) { reported = true })
	if err != nil || !reported || output.Len() != 0 {
		t.Fatalf("refused drop leaked content/markers: %q, %v", output.String(), err)
	}
}
