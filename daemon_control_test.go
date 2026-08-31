package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/daemonctl"
)

// fakeDaemon stands up the REAL handler on a REAL unix socket, with the two side effects (answer /
// decline) captured instead of performed. Everything under test — validation, the proof-of-surfacing
// digest, the atomic Take — is the code the daemon runs.
type fakeDaemon struct {
	q        *consultQueue
	client   daemonctl.Client
	answered []string
	declined []string
	denyErr  error
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	// A short temp path on purpose: a unix socket path is capped near 104 bytes, and t.TempDir()'s
	// per-test name can push a nested path over it.
	dir, err := os.MkdirTemp("", "ptlnctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeDaemon{q: newConsultQueue(filepath.Join(dir, "pending.json"))}
	sock := filepath.Join(dir, "control.sock")
	handle := consultControlHandler("d-me", f.q, consultActions{
		Approve: func(e api.ConsultEvent) { f.answered = append(f.answered, e.ConsultID) },
		Deny: func(e api.ConsultEvent) error {
			if f.denyErr != nil {
				return f.denyErr
			}
			f.declined = append(f.declined, e.ConsultID)
			return nil
		},
	})
	stop, err := daemonctl.Serve(sock, handle)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(stop)
	f.client = daemonctl.Client{Path: sock}
	return f
}

const askQ = "does the limiter retry 429s?"

// The round trip a UI actually performs: list what's waiting, fetch the one you're about to show,
// then approve it. Approving runs the daemon's existing read-only answer path — the UI never answers itself.
func TestControlChannelApproveRoundTrip(t *testing.T) {
	f := newFakeDaemon(t)
	f.q.Add(ev("c1", "partyline", askQ), time.Now().Add(-90*time.Second))

	id, err := f.client.Ping()
	if err != nil || id != "d-me" {
		t.Fatalf("Ping = (%q, %v), want (d-me, nil)", id, err)
	}

	list, err := f.client.ListConsults()
	if err != nil {
		t.Fatalf("ListConsults: %v", err)
	}
	if len(list) != 1 || list[0].ID != "c1" || list[0].Project != "partyline" || list[0].Question != askQ {
		t.Fatalf("list = %+v", list)
	}
	if list[0].WaitingSec < 89 {
		t.Errorf("waiting_sec = %d, want ≈90 (how long the peer has waited)", list[0].WaitingSec)
	}

	got, err := f.client.GetConsult("c1")
	if err != nil {
		t.Fatalf("GetConsult: %v", err)
	}
	if got.Question != askQ {
		t.Errorf("fetched question = %q", got.Question)
	}
	if f.q.Len() != 1 {
		t.Error("fetching to SHOW must not consume the consult")
	}

	if err := f.client.ApproveConsult("c1", got.Question); err != nil {
		t.Fatalf("ApproveConsult: %v", err)
	}
	if len(f.answered) != 1 || f.answered[0] != "c1" {
		t.Fatalf("approve did not run the daemon's answer path: %v", f.answered)
	}
	if f.q.Len() != 0 {
		t.Error("an approved consult must leave the pending set")
	}
	// Replay: the id is gone, so a second approve can't spawn a second (paid) answer turn.
	if err := f.client.ApproveConsult("c1", askQ); err == nil {
		t.Error("re-approving an already-approved consult must be refused")
	}
	if len(f.answered) != 1 {
		t.Errorf("replay spawned another answer turn: %v", f.answered)
	}
}

func TestControlChannelDenyRoundTrip(t *testing.T) {
	f := newFakeDaemon(t)
	f.q.Add(ev("c2", "partyline", askQ), time.Now())
	if err := f.client.DenyConsult("c2", "not now"); err != nil {
		t.Fatalf("DenyConsult: %v", err)
	}
	if len(f.declined) != 1 || f.declined[0] != "c2" {
		t.Fatalf("deny did not reach the control plane: %v", f.declined)
	}
	if f.q.Len() != 0 {
		t.Error("a declined consult must leave the pending set")
	}
	if err := f.client.DenyConsult("c2", "again"); err == nil {
		t.Error("re-denying must be refused")
	}
}

// A decline that never reached the control plane must report the failure: the asker is still waiting,
// and claiming success would hide that.
func TestControlChannelDenySurfacesPostFailure(t *testing.T) {
	f := newFakeDaemon(t)
	f.denyErr = errors.New("503 from the control plane")
	f.q.Add(ev("c3", "partyline", askQ), time.Now())
	err := f.client.DenyConsult("c3", "no")
	if err == nil {
		t.Fatal("want an error when the decline can't be posted")
	}
	if got := err.Error(); !strings.Contains(got, "503") {
		t.Errorf("error must carry the cause, got %q", got)
	}
}

// THE OWNERSHIP WALL, server-side. A local caller cannot invent a consult id or name a teammate's:
// the only ids that exist here are the ones the control plane addressed to THIS daemon (the pending
// set's provenance rule). Both clients get this check because it lives on the socket's server side.
func TestControlChannelRefusesForeignConsultIDs(t *testing.T) {
	f := newFakeDaemon(t)
	f.q.Add(ev("mine", "partyline", askQ), time.Now())

	for _, id := range []string{"someone-elses-consult", "", "mine-but-typo"} {
		if err := f.client.ApproveConsult(id, askQ); err == nil {
			t.Errorf("approve %q must be refused", id)
		}
		if err := f.client.DenyConsult(id, "no"); err == nil {
			t.Errorf("deny %q must be refused", id)
		}
		if _, err := f.client.GetConsult(id); err == nil {
			t.Errorf("get %q must be refused", id)
		}
	}
	if len(f.answered) != 0 || len(f.declined) != 0 {
		t.Fatalf("a foreign id caused an action: answered=%v declined=%v", f.answered, f.declined)
	}
	if f.q.Len() != 1 {
		t.Error("a refused request must not disturb the real pending consult")
	}
}

// Proof-of-surfacing: a one-click "approve" that never showed the question can't produce the digest.
// This is the design guard against a menu item that approves things nobody read.
func TestControlChannelApproveRequiresTheQuestionShown(t *testing.T) {
	f := newFakeDaemon(t)
	f.q.Add(ev("c1", "partyline", askQ), time.Now())

	// No digest at all — the blind-approve shape.
	if _, err := f.client.Do(daemonctl.Request{Op: daemonctl.OpApproveConsult, ID: "c1"}); err == nil {
		t.Error("approving with no digest must be refused")
	}
	// A digest of the wrong text (a stale cached question, or a guess).
	if err := f.client.ApproveConsult("c1", "some other question"); err == nil {
		t.Error("approving with a mismatched digest must be refused")
	}
	if len(f.answered) != 0 {
		t.Fatalf("a blind approve got through: %v", f.answered)
	}
	if f.q.Len() != 1 {
		t.Error("a refused approve must leave the consult pending")
	}
	// And the honest path still works.
	if err := f.client.ApproveConsult("c1", askQ); err != nil {
		t.Errorf("approve after showing the question: %v", err)
	}
}

// Local-only, filesystem-authenticated: the socket must not be readable by other users on the box.
func TestControlSocketIsPrivate(t *testing.T) {
	dir, err := os.MkdirTemp("", "ptlnctl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "control.sock")
	stop, err := daemonctl.Serve(sock, func(daemonctl.Request) daemonctl.Response {
		return daemonctl.Response{OK: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
	stop()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("stopping must remove the socket file")
	}
	// And with nothing listening, a client says "no daemon" rather than failing obscurely.
	if _, err := (daemonctl.Client{Path: sock}).Ping(); !errors.Is(err, daemonctl.ErrNoDaemon) {
		t.Errorf("Ping with no daemon = %v, want ErrNoDaemon", err)
	}
}

// The wire version rides on every request, and an op the daemon doesn't know is refused rather than
// guessed at.
func TestControlChannelRejectsUnknownOp(t *testing.T) {
	f := newFakeDaemon(t)
	res, err := f.client.Do(daemonctl.Request{Op: daemonctl.OpPing})
	if err != nil || res.V != daemonctl.Version {
		t.Fatalf("Do = (%+v, %v)", res, err)
	}
	if _, err := f.client.Do(daemonctl.Request{Op: "make-me-a-sandwich", ID: "c1"}); err == nil {
		t.Error("an unknown op must be refused")
	}
}
