package skillzip

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipWith builds an in-memory zip from a list of (name, mode, body) entries, bypassing Bundle so we can
// forge the hostile entries (traversal, absolute, backslash, symlink) that Bundle would never emit.
func zipWith(t *testing.T, entries []struct {
	name string
	mode os.FileMode
	body string
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			fh.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatalf("CreateHeader %q: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("write %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// Bundle → Unzip should reproduce the tree, keep SKILL.md, and preserve the exec bit on a script.
func TestBundleUnzipRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: t\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := Bundle(src)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	dst := t.TempDir()
	if err := Unzip(data, dst); err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "SKILL.md")); err != nil || !strings.Contains(string(b), "body") {
		t.Fatalf("SKILL.md not restored: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); err != nil {
		t.Fatalf("README.md not restored: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dst, "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("run.sh not restored: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("exec bit lost on run.sh: mode %v", fi.Mode())
	}
}

// Executability is decided by PATH, not the zip's exec bit: a non-script file marked executable in the
// source lands 0644 (so the has_scripts badge, which is path-based, can't be defeated by an exec-bit-on-
// a-data-file bundle), while a script by path/extension lands 0755 even if the source bit was unset.
func TestUnzipExecIsPathBased(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: t\n---\nb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "logo.png"), []byte("PNGDATA"), 0o755); err != nil { // data file, exec bit ON
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "tool.sh"), []byte("echo hi"), 0o644); err != nil { // script by ext, exec bit OFF
		t.Fatal(err)
	}
	data, err := Bundle(src)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	dst := t.TempDir()
	if err := Unzip(data, dst); err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	png, _ := os.Stat(filepath.Join(dst, "logo.png"))
	if png.Mode().Perm()&0o111 != 0 {
		t.Errorf("data file logo.png must NOT be executable despite the source exec bit: %v", png.Mode())
	}
	sh, _ := os.Stat(filepath.Join(dst, "tool.sh"))
	if sh.Mode().Perm()&0o111 == 0 {
		t.Errorf("script tool.sh must be executable by extension: %v", sh.Mode())
	}
}

// IsScriptPath is the single rule shared with the web badge — cover its edge cases directly.
func TestIsScriptPath(t *testing.T) {
	yes := []string{"scripts/run", "a/scripts/x", "deploy.sh", "x.PY", "nested/tool.js", "scripts/nested/deep"}
	no := []string{"SKILL.md", "README", "assets/logo.png", "notes.txt", "scriptsfile", "data.bin", "x.md"}
	for _, p := range yes {
		if !IsScriptPath(p) {
			t.Errorf("IsScriptPath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsScriptPath(p) {
			t.Errorf("IsScriptPath(%q) = true, want false", p)
		}
	}
}

func TestBundleRequiresSkillMd(t *testing.T) {
	if _, err := Bundle(t.TempDir()); err == nil {
		t.Fatal("expected error for a dir with no SKILL.md")
	}
}

// Every hostile entry name must be rejected — these are the zip-slip / traversal boundary.
func TestUnzipRejectsHostileEntries(t *testing.T) {
	cases := []struct {
		label string
		name  string
		mode  os.FileMode
	}{
		{"traversal", "../evil.sh", 0},
		{"deep traversal", "a/../../evil", 0},
		{"absolute", "/etc/passwd", 0},
		{"backslash", "a\\b.sh", 0},
		{"symlink", "link", os.ModeSymlink | 0o777},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			data := zipWith(t, []struct {
				name string
				mode os.FileMode
				body string
			}{{c.name, c.mode, "x"}})
			if err := Unzip(data, t.TempDir()); err == nil {
				t.Fatalf("expected rejection of %q", c.name)
			}
		})
	}
}

func TestUnzipRejectsTooManyFiles(t *testing.T) {
	entries := make([]struct {
		name string
		mode os.FileMode
		body string
	}, MaxFiles+1)
	for i := range entries {
		entries[i].name = "f/" + itoa(i) + ".txt"
		entries[i].body = "x"
	}
	data := zipWith(t, entries)
	if err := Unzip(data, t.TempDir()); err == nil {
		t.Fatalf("expected rejection over %d entries", MaxFiles)
	}
}

// A single entry whose declared uncompressed size is over the per-file cap is rejected before writing.
func TestUnzipRejectsOversizeEntry(t *testing.T) {
	big := strings.Repeat("z", MaxFileBytes+1)
	data := zipWith(t, []struct {
		name string
		mode os.FileMode
		body string
	}{{"big.bin", 0, big}})
	if err := Unzip(data, t.TempDir()); err == nil {
		t.Fatal("expected rejection of an oversize entry")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
