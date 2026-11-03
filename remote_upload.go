package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sync"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// A paired phone sends a photo or file to this computer over its encrypted session, in ordered
// chunks; the computer writes it under a name it chooses and replies with the path, which the phone
// adds to a draft. The phone's own filename is never used as a path. Agreed with #13 codex:
// upload_begin {uploadId,name,mime,size,sha256}, upload_chunk {uploadId,index,bytes},
// upload_finish {uploadId} (replies {uploadId,path}), upload_cancel {uploadId}.

const (
	uploadMaxFile    = 20 << 20  // one file
	uploadMaxPhone   = 200 << 20 // everything one phone has stored; the oldest go first
	uploadMaxChunk   = 32 << 10  // raw bytes per chunk
	uploadMaxOpen    = 2         // uploads one phone may have in progress
	uploadIdle       = 2 * time.Minute
	uploadBytesBurst = 2 * uploadMaxFile // a phone's chunk bytes at once: two whole files, as the phone
	// sends without pausing and fails an upload on a refused chunk
	uploadBytesRate = 4 << 20 // and refilled per second
)

// uploadTypes is the extension each listed type is stored with; any other type (a zip, a script,
// application/octet-stream) is an inert file that keeps a plain extension of its own, or .bin.
var uploadTypes = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/heic": ".heic", "image/heif": ".heif", "image/avif": ".avif", "image/gif": ".gif", "image/webp": ".webp",
	"application/pdf": ".pdf", "text/plain": ".txt", "text/markdown": ".md", "text/csv": ".csv", "application/json": ".json",
}

type upload struct {
	id, name, mime, sha string
	size, got           int64
	next                int
	file                *os.File
	part                string
	sum                 hash.Hash
	last                time.Time
}

type uploadStore struct {
	mu      sync.Mutex
	phones  map[string]map[string]*upload
	budget  map[string]float64 // chunk bytes a phone may still send now
	refill  map[string]time.Time
	baseDir func() string
}

var processUploads = &uploadStore{phones: map[string]map[string]*upload{}, budget: map[string]float64{}, refill: map[string]time.Time{}, baseDir: uploadBaseDir}

type uploadRequest struct {
	UploadID string `json:"uploadId"`
	Name     string `json:"name"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Index    int    `json:"index"`
	Bytes    string `json:"bytes"`
}

// handle runs one upload command for phone route, already authorized as a known phone. It returns
// the reply's status, reason and data.
func (s *uploadStore) handle(route string, m remote.Message, now time.Time) (string, string, any) {
	var req uploadRequest
	if b, err := json.Marshal(m.Data); err != nil || json.Unmarshal(b, &req) != nil {
		return "refused", "invalid upload", nil
	}
	if req.UploadID == "" || len(req.UploadID) > 128 {
		return "refused", "invalid upload id", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now)
	open := s.phones[route]
	u := open[req.UploadID]
	var err error
	var data any
	switch m.Type {
	case "upload_begin":
		err = s.begin(route, req, now)
	case "upload_chunk":
		err = s.chunk(route, u, req, now)
	case "upload_finish":
		var path string
		path, err = s.finish(route, u)
		if err == nil {
			data = map[string]string{"uploadId": req.UploadID, "path": path}
		}
	case "upload_cancel":
		if u != nil {
			s.discard(route, u)
		}
	default:
		err = errors.New("unsupported upload command")
	}
	if err != nil {
		return "refused", err.Error(), nil
	}
	return "ok", "", data
}

func (s *uploadStore) begin(route string, req uploadRequest, now time.Time) error {
	ext, ok := uploadTypes[req.Mime]
	if !ok {
		ext = uploadExtension(req.Name)
	}
	if req.Size <= 0 || req.Size > uploadMaxFile {
		return fmt.Errorf("size must be 1 byte to %d MB", uploadMaxFile>>20)
	}
	if _, err := hex.DecodeString(req.SHA256); err != nil || len(req.SHA256) != 64 {
		return errors.New("invalid sha256")
	}
	if s.phones[route][req.UploadID] != nil {
		return errors.New("upload already started")
	}
	if len(s.phones[route]) >= uploadMaxOpen {
		return fmt.Errorf("at most %d uploads at once", uploadMaxOpen)
	}
	dir, err := s.phoneDir(route)
	if err != nil {
		return err
	}
	if err := s.makeRoom(route, dir, req.Size); err != nil {
		return err
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	part := filepath.Join(dir, hex.EncodeToString(id[:])+ext+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if s.phones[route] == nil {
		s.phones[route] = map[string]*upload{}
	}
	s.phones[route][req.UploadID] = &upload{id: req.UploadID, name: req.Name, mime: req.Mime, sha: req.SHA256, size: req.Size,
		file: f, part: part, sum: sha256.New(), last: now}
	return nil
}

func (s *uploadStore) chunk(route string, u *upload, req uploadRequest, now time.Time) error {
	if u == nil {
		return errors.New("no such upload")
	}
	if req.Index != u.next {
		return fmt.Errorf("chunk %d out of order; expected %d", req.Index, u.next)
	}
	b, err := base64.StdEncoding.DecodeString(req.Bytes)
	if err != nil || len(b) == 0 || len(b) > uploadMaxChunk {
		return errors.New("invalid chunk")
	}
	if u.got+int64(len(b)) > u.size {
		s.discard(route, u)
		return errors.New("more bytes than the upload's size")
	}
	if !s.spend(route, len(b), now) {
		return errors.New("upload rate limit; resend this chunk")
	}
	if _, err := u.file.Write(b); err != nil {
		s.discard(route, u)
		return err
	}
	u.sum.Write(b)
	u.got += int64(len(b))
	u.next++
	u.last = now
	return nil
}

// finish checks the bytes against the begin's size and sha256, then gives the file its final name.
func (s *uploadStore) finish(route string, u *upload) (string, error) {
	if u == nil {
		return "", errors.New("no such upload")
	}
	defer delete(s.phones[route], u.id)
	if err := u.file.Close(); err != nil {
		_ = os.Remove(u.part)
		return "", err
	}
	if u.got != u.size || hex.EncodeToString(u.sum.Sum(nil)) != u.sha {
		_ = os.Remove(u.part)
		return "", errors.New("the file did not arrive intact; send it again")
	}
	final := u.part[:len(u.part)-len(".part")]
	if err := os.Rename(u.part, final); err != nil {
		_ = os.Remove(u.part)
		return "", err
	}
	_ = remoteUploadAudit(route, u)
	return final, nil
}

func (s *uploadStore) discard(route string, u *upload) {
	_ = u.file.Close()
	_ = os.Remove(u.part)
	delete(s.phones[route], u.id)
}
