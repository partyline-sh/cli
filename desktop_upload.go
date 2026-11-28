package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const desktopRetention = 7 * 24 * time.Hour
const desktopMaxBytes = 200 << 20

type desktopUploadHeader struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var desktopUploadID = regexp.MustCompile(`^[a-f0-9]{24}$`)

// This receiver is reached through SSH authentication, never through the relay.
// Its private cache is separate from phone uploads and repository working trees.
func receiveDesktopUpload(base string, input io.Reader, now time.Time) (string, error) {
	in := bufio.NewReader(input)
	line, err := in.ReadSlice('\n') // bounded by the reader's 4 KiB buffer
	if err != nil {
		return "", errors.New("invalid upload header")
	}
	var header desktopUploadHeader
	if json.Unmarshal(line, &header) != nil || header.Size < 1 || header.Size > uploadMaxFile {
		return "", errors.New("file must be 1 byte to 20 MiB")
	}
	if len(header.SHA256) != 64 {
		return "", errors.New("invalid checksum")
	}
	if _, err := hex.DecodeString(header.SHA256); err != nil {
		return "", errors.New("invalid checksum")
	}
	name := header.Name
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00\r\n") || len(name) > 200 {
		return "", errors.New("invalid filename")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return "", errors.New("invalid filename")
		}
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	// Refuse a redirected cache root; os.Root confines every later operation.
	resolved, err := filepath.EvalSymlinks(base)
	abs, absErr := filepath.Abs(base)
	if err != nil || absErr != nil || resolved != abs {
		return "", errors.New("upload cache must not contain symlinks")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.Mkdir("desktop", 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := root.Lstat("desktop")
	if err != nil || !info.IsDir() {
		return "", errors.New("invalid desktop upload directory")
	}
	dir, err := root.OpenRoot("desktop")
	if err != nil {
		return "", err
	}
	defer dir.Close()
	lock, err := dir.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return "", errors.New("another desktop upload is in progress; retry shortly")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := pruneDesktopUploads(dir, now); err != nil {
		return "", err
	}
	var used int64
	err = fs.WalkDir(dir.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if used+header.Size > desktopMaxBytes {
		return "", errors.New("desktop upload cache is full (200 MiB); remove unneeded uploads")
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	folder := hex.EncodeToString(id)
	if err := dir.Mkdir(folder, 0o700); err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = dir.RemoveAll(folder)
		}
	}()
	file, err := dir.OpenFile(filepath.Join(folder, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(in, header.Size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != header.Size || hex.EncodeToString(hash.Sum(nil)) != header.SHA256 {
		return "", errors.New("upload size or checksum mismatch")
	}
	complete = true
	return filepath.Join(abs, "desktop", folder, name), nil
}

func pruneDesktopUploads(root *os.Root, now time.Time) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !desktopUploadID.MatchString(entry.Name()) || !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) > desktopRetention {
			if err := root.RemoveAll(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func desktopUploadReceiveMain() {
	path, err := receiveDesktopUpload(uploadBaseDir(), os.Stdin, time.Now())
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"path": path}); err != nil {
		fatal(fmt.Errorf("upload reply: %w", err))
	}
}
