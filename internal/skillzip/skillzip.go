// Package skillzip is the packaged-skill boundary: it turns a skill DIRECTORY (SKILL.md +
// scripts/references/assets/…) into a zip for upload, and safely unpacks a downloaded zip back onto
// disk. Unzip is a SECURITY boundary — a skill bundle is untrusted input from the control plane, so it
// rejects path traversal, absolute paths, backslash separators, and symlink entries, and caps the file
// count + total size (defends against zip-slip and zip-bombs alike). Both directions share ONE set of
// caps so a bundle that packs is guaranteed to fit the unpack limits.
package skillzip

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Caps — the single source of truth for both Bundle and Unzip. A single file over MaxFileBytes is
// SKIPPED when bundling (and REJECTED when unzipping); a bundle whose total would exceed MaxTotalBytes
// or MaxFiles is an error (we never silently truncate).
const (
	MaxFileBytes  = 5 << 20  // 5 MiB per file
	MaxTotalBytes = 20 << 20 // 20 MiB per bundle
	MaxFiles      = 200      // entries per bundle
)

// scriptExts is the canonical set of "this file is a script" extensions — kept in sync with the web
// importer (skill-bundle-import.ts SCRIPT_EXTS) so the has_scripts badge shown before enabling a skill
// matches exactly which files actually land executable here.
var scriptExts = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".py": true, ".js": true,
	".ts": true, ".rb": true, ".pl": true, ".ps1": true,
}

// IsScriptPath reports whether an entry path is treated as a script: under a scripts/ directory, or a
// known script extension. This is the ONLY thing that makes a materialized file executable (see
// writeEntry) — the zip's own exec bit is deliberately IGNORED, so a data file can't be smuggled in
// with the exec bit set and land 0755 while the path-based has_scripts badge reports "no scripts".
func IsScriptPath(name string) bool {
	name = path.Clean(filepath.ToSlash(name))
	segs := strings.Split(name, "/")
	for _, s := range segs[:len(segs)-1] { // dir segments only (last is the basename)
		if s == "scripts" {
			return true
		}
	}
	return scriptExts[strings.ToLower(path.Ext(name))]
}

// Bundle zips the whole skill directory tree with the skill's files at the zip ROOT (SKILL.md first).
// It SKIPS .git/ and node_modules/ subtrees, .DS_Store, symlinks, non-regular files, and any single
// file larger than MaxFileBytes (warned to stderr). It ERRORS — rather than truncating — if the running
// total would exceed MaxTotalBytes or MaxFiles. Executable bits are preserved via the zip external
// attributes (SetMode) so the receiver can detect scripts. SKILL.md must exist at the dir root.
func Bundle(dir string) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("no SKILL.md in %s: %w", dir, err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var total int64
	var count int

	add := func(relSlash, absPath string, info fs.FileInfo) error {
		if info.Size() > MaxFileBytes {
			fmt.Fprintf(os.Stderr, "  (skipping %s: %d bytes over the %d-byte per-file cap)\n", relSlash, info.Size(), MaxFileBytes)
			return nil
		}
		count++
		if count > MaxFiles {
			return fmt.Errorf("skill bundle exceeds %d files — trim the directory before pushing", MaxFiles)
		}
		total += info.Size()
		if total > MaxTotalBytes {
			return fmt.Errorf("skill bundle exceeds %d bytes total — trim the directory before pushing", MaxTotalBytes)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return err
		}
		fh := &zip.FileHeader{Name: relSlash, Method: zip.Deflate}
		fh.SetMode(info.Mode()) // preserves the exec bit in the external attrs
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		return nil
	}

	// SKILL.md first, so it's the top entry of the zip.
	sk, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	if err := add("SKILL.md", filepath.Join(dir, "SKILL.md"), sk); err != nil {
		return nil, err
	}

	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" || rel == "SKILL.md" {
			return nil // .DS_Store dropped; SKILL.md already added first
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil // never bundle symlinks or devices/sockets
		}
		return add(filepath.ToSlash(rel), p, info)
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unzip safely unpacks a skill bundle into destDir. It is the trust boundary for a control-plane-
// supplied zip: every entry name is rejected if it is absolute, contains a backslash, or escapes destDir
// via "..", and symlink entries are rejected outright (a symlink is how a zip smuggles a write outside
// the tree on extraction). The entry count and total uncompressed size are capped (zip-bomb defense).
// Executable bits are preserved (a script entry lands 0755, a data entry 0644). destDir is created.
func Unzip(data []byte, destDir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("not a valid zip: %w", err)
	}
	if len(zr.File) > MaxFiles {
		return fmt.Errorf("bundle has %d entries — over the %d cap", len(zr.File), MaxFiles)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	var total int64 // ACTUAL bytes written (not the zip's declared sizes — those are attacker-controlled)
	for _, f := range zr.File {
		name := f.Name
		if name == "" {
			continue
		}
		if strings.ContainsRune(name, '\\') {
			return fmt.Errorf("unsafe zip entry %q: backslash separator", name)
		}
		if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe zip entry %q: absolute path", name)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("unsafe zip entry %q: symlink", name)
		}
		clean := path.Clean(name)
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unsafe zip entry %q: path traversal", name)
		}
		target := filepath.Join(destDir, filepath.FromSlash(clean))
		rel, err := filepath.Rel(destDir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe zip entry %q: escapes destination", name)
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		// The DECLARED size is a cheap early reject, but it is attacker-controlled: a zip can claim a
		// tiny size and inflate to a bomb. The real cap is enforced on ACTUAL bytes written below —
		// writeEntry truncates+errors past MaxFileBytes, and we accumulate the real total, so a
		// lying-low central directory can't amplify writes past the caps on the daemon's disk.
		if f.UncompressedSize64 > MaxFileBytes {
			return fmt.Errorf("zip entry %q is %d bytes — over the %d per-file cap", name, f.UncompressedSize64, MaxFileBytes)
		}
		n, err := writeEntry(f, target)
		if err != nil {
			return err
		}
		total += n
		if total > MaxTotalBytes {
			return fmt.Errorf("bundle exceeds %d bytes total (inflated)", MaxTotalBytes)
		}
	}
	return nil
}

// writeEntry copies one file entry to target, creating parent dirs, and returns the number of bytes
// actually written. Executability is decided by PATH (IsScriptPath), NOT the zip's exec bit — so a data
// file can't be smuggled in exec, and the mode matches the has_scripts badge exactly. The copy is bounded
// by LimitReader(MaxFileBytes+1); if the stream inflates past MaxFileBytes the entry LIED about its size,
// so we delete the partial file and error rather than let a lying-low central directory amplify the write.
func writeEntry(f *zip.File, target string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	perm := os.FileMode(0o644)
	if IsScriptPath(f.Name) {
		perm = 0o755
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return 0, err
	}
	n, cerr := io.Copy(out, io.LimitReader(rc, MaxFileBytes+1))
	closeErr := out.Close()
	if cerr != nil {
		return n, cerr
	}
	if closeErr != nil {
		return n, closeErr
	}
	if n > MaxFileBytes {
		_ = os.Remove(target) // don't leave the oversized partial on disk
		return n, fmt.Errorf("zip entry %q inflated past the %d-byte per-file cap (lying header)", f.Name, MaxFileBytes)
	}
	return n, os.Chmod(target, perm) // O_CREATE mode is umask-masked; force the exec bit back on for scripts
}
