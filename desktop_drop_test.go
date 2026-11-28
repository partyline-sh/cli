package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type desktopByteReader struct{ io.Reader }

func (r desktopByteReader) Read(b []byte) (int, error) { return r.Reader.Read(b[:1]) }

func TestDesktopDropTransfersBeforeInsertingWithoutEnter(t *testing.T) {
	local := filepath.Join(t.TempDir(), "context notes.md")
	if err := os.WriteFile(local, []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var sent []string
	input := "draft " + pasteStart + quoteDesktopPath(local) + pasteEnd + " more"
	err := forwardDesktopInput(desktopByteReader{strings.NewReader(input)}, &output, func(path string) (string, error) {
		if output.String() != "draft " {
			t.Fatal("forwarded path before upload completed")
		}
		sent = append(sent, path)
		return "/host/context notes.md", nil
	}, func(err error) { t.Fatal(err) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sent, []string{local}) {
		t.Fatalf("uploaded %v", sent)
	}
	want := "draft " + pasteStart + "'/host/context notes.md'" + pasteEnd + " more"
	if output.String() != want {
		t.Fatalf("got %q, want %q", output.String(), want)
	}
}

func TestDesktopDropPreservesTextAndRejectsFailedUploads(t *testing.T) {
	for _, input := range []string{"ordinary\x03keys", pasteStart + "hello\nworld" + pasteEnd, pasteStart + "/missing/file" + pasteEnd} {
		var output bytes.Buffer
		err := forwardDesktopInput(desktopByteReader{strings.NewReader(input)}, &output, func(string) (string, error) { t.Fatal("uploaded text"); return "", nil }, func(err error) { t.Fatal(err) })
		if err != nil || output.String() != input {
			t.Fatalf("got %q, %v", output.String(), err)
		}
	}
	local := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	reported := false
	err := forwardDesktopInput(strings.NewReader(pasteStart+local+pasteEnd), &output, func(string) (string, error) { return "", errors.New("offline") }, func(error) { reported = true })
	if err != nil || !reported || output.String() != "" {
		t.Fatalf("failed upload leaked input: %q, %v", output.String(), err)
	}
}

func TestDesktopDropParsesEscapedPathsWithoutEvaluation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a b'$(touch nope).md")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{quoteDesktopPath(path), strings.ReplaceAll(strings.ReplaceAll(path, " ", "\\ "), "'", "\\'")} {
		if got := desktopDropPaths(text); !reflect.DeepEqual(got, []string{path}) {
			t.Fatalf("parse %q: %v", text, got)
		}
	}
	for _, text := range []string{"read " + quoteDesktopPath(path), "'unterminated", "relative.md", "file://other-host/file"} {
		if got := desktopDropPaths(text); len(got) != 0 {
			t.Fatalf("accepted mixed or invalid paste %q", text)
		}
	}
}

func TestDesktopDropRejectsSensitivePathsWithoutForwarding(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{".env", "id.pem"} {
		path := filepath.Join(base, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		reported := false
		err := forwardDesktopInput(strings.NewReader(pasteStart+quoteDesktopPath(path)+pasteEnd), &output,
			func(string) (string, error) { t.Fatal("uploaded sensitive path"); return "", nil }, func(error) { reported = true })
		if err != nil || !reported || output.Len() != 0 {
			t.Fatalf("rejected path forwarded: %q, %v", output.String(), err)
		}
	}
	for _, path := range []string{"/Users/darcy/.ssh/id_ed25519", "/Users/darcy/.aws/credentials", "/Users/darcy/Library/Keychains/login.keychain-db"} {
		if !desktopSensitivePath(path) {
			t.Fatalf("did not block %s", path)
		}
	}
}
