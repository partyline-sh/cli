package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopUploadSSHReceiverHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "desktop-upload-receiver" {
		return
	}
	// The repository's test init creates a new HOME in each subprocess. macOS
	// spells that path through /var, which is an OS symlink to /private/var.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatal(err)
	}
	desktopUploadReceiveMain()
	os.Exit(0)
}

func TestDesktopUploadSSHTransfer(t *testing.T) {
	bin := desktopUploadTestDir(t)
	t.Setenv("XDG_CACHE_HOME", desktopUploadTestDir(t))
	t.Setenv("HOME", desktopUploadTestDir(t))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The fake SSH transports stdin/stdout to the real receiver in another process.
	script := "#!/bin/sh\nexec " + quoteDesktopPath(exe) + " -test.run=^TestDesktopUploadSSHReceiverHelper$ -- desktop-upload-receiver\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(desktopUploadTestDir(t), "context.md")
	if err := os.WriteFile(source, []byte("end to end\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := sendDesktopUpload("test-host", source)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "end to end\n" {
		t.Fatalf("received %q: %v", data, err)
	}
}

func desktopUploadTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func desktopUploadWire(name string, data []byte) []byte {
	sum := sha256.Sum256(data)
	header, _ := json.Marshal(desktopUploadHeader{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	return append(append(header, '\n'), data...)
}

func TestDesktopUploadStoresUniquePrivateFiles(t *testing.T) {
	base := desktopUploadTestDir(t)
	for range 2 {
		path, err := receiveDesktopUpload(base, bytes.NewReader(desktopUploadWire("context.md", []byte("context"))), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "context" {
			t.Fatalf("stored bytes: %q, %v", data, err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions: %v", info.Mode())
		}
		if !strings.HasPrefix(path, filepath.Join(base, "desktop")+string(os.PathSeparator)) {
			t.Fatal(path)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(base, "desktop"))
	if len(entries) != 3 {
		t.Fatalf("expected two unique folders plus lock, got %d", len(entries))
	}
}

func TestDesktopUploadRejectsTraversalCorruptionAndOversize(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "nested/file", "..", "a\\b", "line\nbreak"} {
		t.Run(name, func(t *testing.T) {
			if _, err := receiveDesktopUpload(desktopUploadTestDir(t), bytes.NewReader(desktopUploadWire(name, []byte("x"))), time.Now()); err == nil {
				t.Fatal("accepted invalid filename")
			}
		})
	}
	for _, wire := range [][]byte{
		desktopUploadWire("empty", nil),
		append(desktopUploadWire("extra", []byte("x")), 'y'),
		bytes.Replace(desktopUploadWire("corrupt", []byte("x")), []byte("\nx"), []byte("\ny"), 1),
		[]byte("{\"name\":\"big\",\"size\":20971521}\n"),
		[]byte(strings.Repeat("x", 4096) + "\n"),
	} {
		base := desktopUploadTestDir(t)
		if _, err := receiveDesktopUpload(base, bytes.NewReader(wire), time.Now()); err == nil {
			t.Fatal("accepted invalid content")
		}
		entries, _ := os.ReadDir(filepath.Join(base, "desktop"))
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatal("left partial upload")
			}
		}
	}
}

func TestDesktopUploadRejectsSymlinkAndPrunesOnlyExpiredUploads(t *testing.T) {
	base, outside := desktopUploadTestDir(t), desktopUploadTestDir(t)
	if err := os.Symlink(outside, filepath.Join(base, "desktop")); err != nil {
		t.Fatal(err)
	}
	if _, err := receiveDesktopUpload(base, bytes.NewReader(desktopUploadWire("file", []byte("x"))), time.Now()); err == nil {
		t.Fatal("followed symlink")
	}
	if err := os.Remove(filepath.Join(base, "desktop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "desktop"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Join(base, "desktop"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	now := time.Now()
	for _, name := range []string{strings.Repeat("a", 24), "keep-me"} {
		if err := root.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := root.Symlink(outside, filepath.Join(name, "outside")); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-8 * 24 * time.Hour)
		if err := root.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruneDesktopUploads(root, now); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(strings.Repeat("a", 24)); !os.IsNotExist(err) {
		t.Fatal("expired upload kept")
	}
	if _, err := root.Stat("keep-me"); err != nil {
		t.Fatal("deleted unrelated directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("deleted through symlink")
	}
}

func TestDesktopUploadQuota(t *testing.T) {
	base := desktopUploadTestDir(t)
	if err := os.Mkdir(filepath.Join(base, "desktop"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(base, "desktop", "retained"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(desktopMaxBytes); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := receiveDesktopUpload(base, bytes.NewReader(desktopUploadWire("file", []byte("x"))), time.Now()); err == nil {
		t.Fatal("exceeded quota")
	}
}
