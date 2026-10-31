package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The upload store's limits and housekeeping: where files go, how much a phone may keep and send,
// what is cleaned up, and the audit line. The commands are in remote_upload.go.

// uploadBaseDir is outside ~/.partyline, which holds pairing keys and tokens and which agents are
// denied: the path goes to an agent session, so the file must be somewhere it may read.
func uploadBaseDir() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Caches", "sh.partyline", "uploads")
	}
	if d := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); d != "" {
		return filepath.Join(d, "partyline", "uploads")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "partyline", "uploads")
}

// cancelAll drops a phone's unfinished uploads, when its session ends.
func (s *uploadStore) cancelAll(route string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.phones[route] {
		s.discard(route, u)
	}
}

// expire drops uploads that have had no chunk for uploadIdle.
func (s *uploadStore) expire(now time.Time) {
	for route, open := range s.phones {
		for _, u := range open {
			if now.Sub(u.last) > uploadIdle {
				s.discard(route, u)
			}
		}
	}
}

// spend takes n bytes from the phone's budget, which refills at uploadBytesRate a second.
func (s *uploadStore) spend(route string, n int, now time.Time) bool {
	last, ok := s.refill[route]
	if !ok {
		s.budget[route], last = uploadBytesBurst, now
	}
	s.budget[route] = math.Min(uploadBytesBurst, s.budget[route]+now.Sub(last).Seconds()*uploadBytesRate)
	s.refill[route] = now
	if s.budget[route] < float64(n) {
		return false
	}
	s.budget[route] -= float64(n)
	return true
}

func (s *uploadStore) phoneDir(route string) (string, error) {
	if len(route) != 32 {
		return "", errors.New("invalid phone")
	}
	if _, err := hex.DecodeString(route); err != nil {
		return "", errors.New("invalid phone")
	}
	dir := filepath.Join(s.baseDir(), route)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

// makeRoom deletes the phone's oldest finished files until size more bytes fit under uploadMaxPhone,
// and any partial file no upload in progress owns.
func (s *uploadStore) makeRoom(route, dir string, size int64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	open := map[string]bool{}
	for _, u := range s.phones[route] {
		total += u.size // in progress: reserved at their full size
		open[u.part] = true
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || open[path] {
			continue
		}
		if filepath.Ext(e.Name()) == ".part" { // left by a crash: no upload of this phone is writing it
			_ = os.Remove(path)
			continue
		}
		files = append(files, file{path, info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for len(files) > 0 && total+size > uploadMaxPhone {
		_ = os.Remove(files[0].path)
		total -= files[0].size
		files = files[1:]
	}
	if total+size > uploadMaxPhone {
		return errors.New("no room: other uploads in progress")
	}
	return nil
}

// remoteUploadAudit records an upload in events.jsonl: who, how big, what type. Never its contents.
func remoteUploadAudit(route string, u *upload) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(struct {
		At      time.Time `json:"at"`
		Action  string    `json:"action"`
		PhoneID string    `json:"phoneId"`
		Mime    string    `json:"mime"`
		Size    int64     `json:"size"`
	}{time.Now().UTC(), "upload", route, u.mime, u.size})
}

// uploadExtension is the extension a file of an unlisted type keeps: its own when short and plain
// and not one macOS would run or install when opened, else .bin. Files are never executable anyway.
func uploadExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if !uploadPlainExt.MatchString(ext) || uploadRunnable[ext] {
		return ".bin"
	}
	return ext
}

var uploadPlainExt = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)

var uploadRunnable = map[string]bool{".app": true, ".command": true, ".sh": true, ".zsh": true, ".bash": true, ".pkg": true,
	".mpkg": true, ".dmg": true, ".exe": true, ".bat": true, ".ps1": true, ".scpt": true, ".terminal": true,
	".workflow": true, ".jar": true, ".tool": true, ".webloc": true, ".inetloc": true}
