package phoneterm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPTYSessionIsolationAndReattach(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	r := Registry{Root: t.TempDir(), Socket: fmt.Sprintf("ptln-test-phone-%d", os.Getpid())}
	defer command(r.Socket, "kill-server").Run()
	t.Setenv("TMUX", "not-a-real-parent")
	s, err := r.Create("phone-a", "request-a", "/bin/bash", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Create("phone-a", "request-a", "/bin/bash", 80, 24)
	if err != nil || again.ID != s.ID {
		t.Fatal("create was not idempotent", err)
	}
	if _, err = r.Attach("phone-b", s.ID, 80, 24); err == nil {
		t.Fatal("other phone attached")
	}
	a, err := r.Attach("phone-a", s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	output := make(chan []byte, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer a.Wait()
		buf := make([]byte, 8192)
		for {
			n, e := a.Master.Read(buf)
			if n > 0 {
				select {
				case output <- bytes.Clone(buf[:n]):
				case <-time.After(time.Second):
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	if _, err = io.WriteString(a.Master, "printf 'PHONE-%s\\n' streamed\r"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	var seen []byte
read:
	for {
		select {
		case chunk := <-output:
			seen = append(seen, chunk...)
			if bytes.Contains(seen, []byte("PHONE-streamed")) {
				break read
			}
		case <-deadline:
			a.Close()
			t.Fatalf("no shell output: %q", seen)
		}
	}
	if _, err = r.Attach("phone-a", s.ID, 80, 24); err == nil {
		t.Fatal("second attachment accepted")
	}
	if _, err = io.WriteString(a.Master, "printf '\\033[?1049h\\033[HALT-RESTORED'; read -r HOLD\r"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(3 * time.Second)
	for {
		screen, e := command(r.Socket, "capture-pane", "-p", "-t", Prefix+s.ID).Output()
		if e == nil && bytes.HasPrefix(screen, []byte("ALT-RESTORED")) {
			break
		}
		if time.Now().After(until) {
			a.Close()
			t.Fatalf("alternate screen not active: %s (%v)", screen, e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = a.Resize(55, 18); err != nil {
		t.Fatal(err)
	}
	a.Close()
	<-done
	if err = command(r.Socket, "has-session", "-t", Prefix+s.ID).Run(); err != nil {
		t.Fatal("detach ended shell", err)
	}
	second, err := r.Attach("phone-a", s.ID, 55, 18)
	if err != nil {
		t.Fatal(err)
	}
	redraw := make(chan []byte, 1)
	go func() { defer second.Wait(); raw, _ := io.ReadAll(io.LimitReader(second.Master, 8192)); redraw <- raw }()
	// Wait for the fresh attachment to draw, then close to make the read finite.
	time.Sleep(100 * time.Millisecond)
	second.Close()
	select {
	case raw := <-redraw:
		if !bytes.Contains(raw, []byte("ALT-RESTORED")) {
			t.Fatalf("reattach did not restore output: %q", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reattach did not drain")
	}
	if err = r.End("phone-a", s.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := r.List("phone-a")
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if _, err = os.Stat(filepath.Join(r.Root, "sessions.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryLimitAndDetachedExpiry(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	r := Registry{Root: t.TempDir(), Socket: fmt.Sprintf("ptln-test-phone-limit-%d", os.Getpid())}
	defer command(r.Socket, "kill-server").Run()
	for i := 0; i < MaxSessions; i++ {
		if _, err := r.Create("owner", fmt.Sprint(i), "/bin/bash", 80, 24); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Create("owner", "ninth", "/bin/bash", 80, 24); err == nil {
		t.Fatal("ninth terminal accepted")
	}
	if err := r.Cleanup(time.Now().Add(Retention + time.Second)); err != nil {
		t.Fatal(err)
	}
	rows, err := r.List("owner")
	if err != nil || len(rows) != 0 {
		t.Fatal("detached sessions were not expired", rows, err)
	}
}
