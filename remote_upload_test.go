package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

const testPhone = "0123456789abcdef0123456789abcdef"

func testUploads(t *testing.T) (*uploadStore, string) {
	t.Helper()
	base := t.TempDir()
	return &uploadStore{phones: map[string]map[string]*upload{}, budget: map[string]float64{}, refill: map[string]time.Time{},
		baseDir: func() string { return base }}, base
}

func uploadMsg(kind string, data map[string]any) remote.Message {
	return remote.Message{Type: kind, ID: kind, Data: data}
}

// send uploads body as id in 32 KB chunks and returns finish's status, reason and path.
func send(t *testing.T, s *uploadStore, id, name, mime string, body []byte, now time.Time) (string, string, string) {
	t.Helper()
	sum := sha256.Sum256(body)
	if st, why, _ := s.handle(testPhone, uploadMsg("upload_begin", map[string]any{"uploadId": id, "name": name, "mime": mime,
		"size": len(body), "sha256": hex.EncodeToString(sum[:])}), now); st != "ok" {
		return st, why, ""
	}
	for i := 0; i*uploadMaxChunk < len(body); i++ {
		end := min((i+1)*uploadMaxChunk, len(body))
		if st, why, _ := s.handle(testPhone, uploadMsg("upload_chunk", map[string]any{"uploadId": id, "index": i,
			"bytes": base64.StdEncoding.EncodeToString(body[i*uploadMaxChunk : end])}), now); st != "ok" {
			return st, why, ""
		}
	}
	st, why, data := s.handle(testPhone, uploadMsg("upload_finish", map[string]any{"uploadId": id}), now)
	if st != "ok" {
		return st, why, ""
	}
	return st, why, data.(map[string]string)["path"]
}

// A file arrives intact under a name the computer chose, in the phone's own private directory,
// readable and never executable; the phone's filename is never a path.
func TestUploadArrivesIntactUnderItsOwnName(t *testing.T) {
	s, base := testUploads(t)
	body := []byte(strings.Repeat("a photo's bytes ", 5000)) // 80 KB: three chunks
	st, why, path := send(t, s, "u1", "../../etc/passwd.png", "image/png", body, time.Now())
	if st != "ok" {
		t.Fatalf("upload refused: %s", why)
	}
	if filepath.Dir(path) != filepath.Join(base, testPhone) || !strings.HasSuffix(path, ".png") || strings.Contains(path, "passwd") {
		t.Errorf("stored at %s", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(body) {
		t.Fatalf("contents differ: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v", di.Mode().Perm())
	}
	if parts, _ := filepath.Glob(filepath.Join(base, testPhone, "*.part")); len(parts) != 0 {
		t.Errorf("a partial file was left: %v", parts)
	}
}

func TestUploadRefusals(t *testing.T) {
	now := time.Now()
	s, base := testUploads(t)
	// An unlisted type is kept as an inert file: its own plain extension, or .bin if runnable.
	kept, _ := testUploads(t)
	if _, why, path := send(t, kept, "x", "a.exe", "application/x-msdownload", []byte("MZ"), now); filepath.Ext(path) != ".bin" {
		t.Errorf("a runnable file was stored as %q (%s)", path, why)
	}
	if _, why, path := send(t, kept, "z", "src.zip", "application/zip", []byte("PK"), now); filepath.Ext(path) != ".zip" {
		t.Errorf("a zip was stored as %q (%s)", path, why)
	}
	if st, _, _ := s.handle(testPhone, uploadMsg("upload_begin", map[string]any{"uploadId": "big", "mime": "image/png",
		"size": uploadMaxFile + 1, "sha256": strings.Repeat("0", 64)}), now); st == "ok" {
		t.Error("a file over the cap was accepted")
	}
	// A corrupted transfer: the bytes do not match the declared sha256.
	sum := sha256.Sum256([]byte("what was meant"))
	s.handle(testPhone, uploadMsg("upload_begin", map[string]any{"uploadId": "bad", "mime": "text/plain", "size": 14, "sha256": hex.EncodeToString(sum[:])}), now)
	s.handle(testPhone, uploadMsg("upload_chunk", map[string]any{"uploadId": "bad", "index": 0, "bytes": base64.StdEncoding.EncodeToString([]byte("what was sent!"))}), now)
	if st, why, _ := s.handle(testPhone, uploadMsg("upload_finish", map[string]any{"uploadId": "bad"}), now); st == "ok" || !strings.Contains(why, "intact") {
		t.Errorf("a mismatched file was kept: %s %s", st, why)
	}
	// Out of order, and unknown uploads.
	s.handle(testPhone, uploadMsg("upload_begin", map[string]any{"uploadId": "ooo", "mime": "text/plain", "size": 4, "sha256": strings.Repeat("0", 64)}), now)
	if st, why, _ := s.handle(testPhone, uploadMsg("upload_chunk", map[string]any{"uploadId": "ooo", "index": 1, "bytes": "YWJjZA=="}), now); st == "ok" || !strings.Contains(why, "out of order") {
		t.Errorf("an out-of-order chunk was taken: %s", why)
	}
	if st, _, _ := s.handle(testPhone, uploadMsg("upload_chunk", map[string]any{"uploadId": "nope", "index": 0, "bytes": "YQ=="}), now); st == "ok" {
		t.Error("a chunk for no upload was taken")
	}
	files, _ := os.ReadDir(filepath.Join(base, testPhone))
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".part") {
			t.Errorf("a refused upload left %s", f.Name())
		}
	}
}

// Unfinished uploads end with their session or after two idle minutes; at most two at once.
func TestUploadLimitsAndCleanup(t *testing.T) {
	now := time.Now()
	s, base := testUploads(t)
	begin := func(id string, at time.Time) string {
		st, why, _ := s.handle(testPhone, uploadMsg("upload_begin", map[string]any{"uploadId": id, "mime": "text/plain", "size": 10, "sha256": strings.Repeat("0", 64)}), at)
		if st != "ok" {
			return why
		}
		return ""
	}
	begin("a", now)
	begin("b", now)
	if why := begin("c", now); !strings.Contains(why, "at most") {
		t.Errorf("a third upload at once: %q", why)
	}
	s.cancelAll(testPhone)
	if parts, _ := filepath.Glob(filepath.Join(base, testPhone, "*.part")); len(parts) != 0 || len(s.phones[testPhone]) != 0 {
		t.Errorf("a disconnect left %v", parts)
	}
	begin("d", now)
	begin("e", now.Add(3*time.Minute)) // a later command expires the idle one
	if s.phones[testPhone]["d"] != nil {
		t.Error("an idle upload was kept")
	}
}

// A phone's stored files stay under uploadMaxPhone: the oldest are deleted to make room, and partial
// files left by a crash are deleted rather than escaping the count.
func TestUploadQuotaDropsTheOldest(t *testing.T) {
	s, base := testUploads(t)
	dir := filepath.Join(base, testPhone)
	_ = os.MkdirAll(dir, 0o700)
	old := filepath.Join(dir, "old.png")
	_ = os.WriteFile(old, make([]byte, uploadMaxPhone-1000), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	orphan := filepath.Join(dir, "crashed.png.part") // left by a crash: counted by no upload
	_ = os.WriteFile(orphan, make([]byte, 1000), 0o600)
	if st, why, _ := send(t, s, "new", "n.txt", "text/plain", []byte(strings.Repeat("x", 5000)), time.Now()); st != "ok" {
		t.Fatalf("refused: %s", why)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the oldest file was not removed to make room")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("an orphaned partial file was kept")
	}
}

// Files of no declared type keep a plain extension; anything macOS would run becomes .bin.
func TestUploadExtension(t *testing.T) {
	for name, want := range map[string]string{"report.zip": ".zip", "notes.LOG": ".log", "run.command": ".bin", "x.app": ".bin",
		"setup.sh": ".bin", "noext": ".bin", "weird.ab$c": ".bin", "a.verylongextension": ".bin"} {
		if got := uploadExtension(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

// Chunk bytes are limited per phone: past the burst, a chunk is refused to be resent.
func TestUploadByteBudget(t *testing.T) {
	s, _ := testUploads(t)
	now := time.Now()
	if !s.spend(testPhone, uploadBytesBurst, now) || s.spend(testPhone, 1, now) {
		t.Fatal("the burst was not enforced")
	}
	if !s.spend(testPhone, uploadBytesRate/2, now.Add(500*time.Millisecond)) {
		t.Error("the budget did not refill")
	}
}

// Through the phone input path: an unknown phone cannot upload; a known one can, and the chunks of
// a large file are not refused by the 20-a-second input limit. The file lands outside ~/.partyline.
func TestUploadThroughThePhoneInputPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	store, route := remoteInputFixture(t)
	now := time.Now()
	body := make([]byte, 1<<20) // 32 chunks, all in the same second
	sum := sha256.Sum256(body)
	call := func(phone, kind, id string, data map[string]any) remote.Message {
		return store.handle(phone, remote.Message{Type: kind, ID: id, Data: data}, now)
	}
	if r := call("ffffffffffffffffffffffffffffffff", "upload_begin", "x", map[string]any{"uploadId": "u", "mime": "text/plain", "size": 1, "sha256": strings.Repeat("0", 64)}); r.Status == "ok" {
		t.Fatal("an unknown phone began an upload")
	}
	if r := call(route, "upload_begin", "b", map[string]any{"uploadId": "u", "name": "dump.bin", "mime": "application/octet-stream", "size": len(body), "sha256": hex.EncodeToString(sum[:])}); r.Status != "ok" {
		t.Fatalf("begin: %+v", r)
	}
	for i := 0; i*uploadMaxChunk < len(body); i++ {
		r := call(route, "upload_chunk", "c"+string(rune('A'+i)), map[string]any{"uploadId": "u", "index": i, "bytes": base64.StdEncoding.EncodeToString(body[i*uploadMaxChunk : (i+1)*uploadMaxChunk])})
		if r.Status != "ok" {
			t.Fatalf("chunk %d: %+v", i, r)
		}
	}
	r := call(route, "upload_finish", "f", map[string]any{"uploadId": "u"})
	path, _ := r.Data.(map[string]string)["path"]
	if r.Status != "ok" || strings.Contains(path, "/.partyline/") || !strings.Contains(path, route) {
		t.Fatalf("finish: %+v", r)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != int64(len(body)) {
		t.Errorf("stored file: %v", err)
	}
}

// A file at the size cap, sent as fast as the phone's four-chunk window allows (here all at one
// instant), is not refused by the byte budget: the phone fails the whole upload on a refused chunk.
func TestUploadFullFileFitsTheBudget(t *testing.T) {
	s, _ := testUploads(t)
	body := make([]byte, uploadMaxFile)
	if st, why, _ := send(t, s, "max", "big.bin", "application/octet-stream", body, time.Now()); st != "ok" {
		t.Fatalf("a %d MB file was refused: %s", uploadMaxFile>>20, why)
	}
}
