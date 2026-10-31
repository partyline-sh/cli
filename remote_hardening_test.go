package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRemoteRevokeOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := relayConfig{URL: "https://relay.test", MachineID: strings.Repeat("ab", 16), RPID: "relay.test"}
	if err := saveRelay(c, strings.Repeat("cd", 32)); err != nil {
		t.Fatal(err)
	}
	p := remotePhone{ID: strings.Repeat("ef", 16), Name: "lost"}
	if err := writePhones([]remotePhone{p}, "paired", p); err != nil {
		t.Fatal(err)
	}
	old, oldDial := relayHTTP, relayDialHTTP
	t.Cleanup(func() { relayHTTP = old; relayDialHTTP = oldDial })
	offlineCalls := 0
	relayHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		offlineCalls++
		phones, err := remotePhones()
		if err != nil || len(phones) != 0 {
			t.Errorf("relay called before local revoke: %v %v", phones, err)
		}
		return nil, errors.New("offline")
	})}
	if err := revokeRemotePhone(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if offlineCalls != 1 {
		t.Fatalf("offline relay calls: %d", offlineCalls)
	}
	phones, err := remotePhones()
	if err != nil || len(phones) != 0 {
		t.Fatalf("phones=%v err=%v", phones, err)
	}
	pending, err := pendingRemoteRevokes()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	info, err := os.Stat(filepath.Join(relayDir(), "pending-revokes.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	events, err := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
	if err != nil || !strings.Contains(string(events), `"action":"revoked"`) {
		t.Fatal(string(events), err)
	}
	calls := 0
	relayHTTP = &http.Client{}
	relayHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/device-revoke" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["phone"] != p.ID || body["id"] != c.MachineID || body["token"] != strings.Repeat("cd", 32) {
			t.Errorf("revoke payload: %v %v", body, err)
		}
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})
	relayDialHTTP = &http.Client{Transport: &relayFakeTransport{hellos: make(chan []byte, 1)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = relaySession(ctx, c, strings.Repeat("cd", 32))
	pending, err = pendingRemoteRevokes()
	if err != nil || len(pending) != 0 || calls != 1 {
		t.Fatalf("pending=%v calls=%d err=%v", pending, calls, err)
	}
}

func TestRemotePairSocket(t *testing.T) {
	t.Run("concurrent approval despite garbage and incomplete request", TestRemotePairApprovalHandshake)
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Dir(pairApprovalSocket())
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := ensurePairApprovalDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Fatal(info)
	}
	l, err := listenPairApproval()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listenPairApproval(); err == nil {
		t.Fatal("active socket replaced")
	}
	l.Close()
	if err := os.WriteFile(pairApprovalSocket(), []byte("squatted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenPairApproval(); err == nil {
		t.Fatal("non-socket path replaced")
	}
	os.Remove(pairApprovalSocket())
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := listenPairApproval(); err == nil {
		t.Fatal("unsafe mode accepted")
	}
	if _, err := requestPairApproval(context.Background(), remotePhone{}, pairEnrollment{}, nil); err == nil || !strings.Contains(err.Error(), "unsafe pairing socket directory") {
		t.Fatalf("unsafe dial error: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if err := ensurePairApprovalDir(); err == nil {
		t.Fatal("symlink accepted")
	}
}

func injectPairSocketClients(t *testing.T) {
	t.Helper()
	garbage, err := net.Dial("unix", pairApprovalSocket())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = garbage.Write([]byte("garbage\n"))
	garbage.Close()
	stalled, err := net.Dial("unix", pairApprovalSocket())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = stalled.Write([]byte(`{"phone":`))
	t.Cleanup(func() { stalled.Close() })
}

func TestRemotePairSocketSquatter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(pairApprovalSocket())) })
	l, err := listenPairApproval()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	received := make(chan []byte, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		nonce := make([]byte, 32)
		io.ReadFull(c, nonce)
		c.Write(make([]byte, 32)) // Cannot prove possession of the independent challenge.
		rest, _ := io.ReadAll(c)
		received <- rest
	}()
	_, err = requestPairApproval(context.Background(), remotePhone{Name: "secret enrollment"}, pairEnrollment{}, []byte(strings.Repeat("x", 32)))
	if err == nil || !strings.Contains(err.Error(), "unauthenticated pairing popup") {
		t.Fatalf("error: %v", err)
	}
	if rest := <-received; len(rest) != 0 {
		t.Fatalf("enrollment leaked: %q", rest)
	}
}

func TestRemoteRevokeBrokenQueue(t *testing.T) {
	old := relayHTTP
	t.Cleanup(func() { relayHTTP = old })
	relayHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}

	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			c := relayConfig{URL: "https://relay.test", RPID: "relay.test", MachineID: strings.Repeat("ab", 16)}
			if err := saveRelay(c, strings.Repeat("cd", 32)); err != nil {
				t.Fatal(err)
			}
			p := remotePhone{ID: strings.Repeat("ef", 16)}
			if err := writePhones([]remotePhone{p}, "paired", p); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(relayDir(), "pending-revokes.json")
			if directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := revokeRemotePhone(context.Background(), p.ID); err != nil {
				t.Fatal(err)
			}
			phones, err := remotePhones()
			if err != nil || len(phones) != 0 {
				t.Fatalf("phones=%v err=%v", phones, err)
			}
		})
	}
}

func TestRemoteRevokeFlushFailureKeepsSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := relayConfig{URL: "https://relay.test", RPID: "relay.test", MachineID: strings.Repeat("ab", 16)}
	if err := saveRelay(c, strings.Repeat("cd", 32)); err != nil {
		t.Fatal(err)
	}
	if err := savePendingRemoteRevokes([]remotePhone{{ID: strings.Repeat("ef", 16)}, {ID: strings.Repeat("ac", 16)}}); err != nil {
		t.Fatal(err)
	}
	old, oldDial := relayHTTP, relayDialHTTP
	t.Cleanup(func() { relayHTTP = old; relayDialHTTP = oldDial })
	calls := 0
	relayHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		status := 400
		if calls == 2 {
			status = 200
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}
	if err := flushRemoteRevokes(context.Background(), c, "token"); err == nil {
		t.Fatal("expected rejection")
	}
	pending, err := pendingRemoteRevokes()
	if err != nil || len(pending) != 1 || !pending[0].RevokeRejected || calls != 2 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	// A failed HTTP flush must not end the WebSocket after its hello acknowledgment.
	if err := os.WriteFile(filepath.Join(relayDir(), "pending-revokes.json"), []byte(`[{"id":"retry"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	relayDialHTTP = &http.Client{Transport: &relayFakeTransport{hellos: make(chan []byte, 1)}}
	// The fake transport ends the session with EOF; the deadline only stops a hang. At 200ms a busy
	// CI runner hit it before the handshake finished.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = relaySession(ctx, c, strings.Repeat("cd", 32))
	if err == nil || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("session failed before entering read loop: %v", err)
	}
}

// A phone's session failure is recorded, not dropped, and the record stays at the last 200 lines.
func TestRemoteSessionLogKeepsTheLatestFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		remoteSessionLog("phone-a", fmt.Errorf("handshake failed %d", i))
	}
	b, err := os.ReadFile(filepath.Join(relayDir(), "session.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 200 || !strings.HasSuffix(lines[199], "phone phone-a: handshake failed 249") || !strings.HasSuffix(lines[0], "handshake failed 50") {
		t.Errorf("%d lines; first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
}
