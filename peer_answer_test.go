package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/daemonctl"
)

type fakeConsults struct {
	out []api.Consult
	err error
	dir string
}

func (f *fakeConsults) ListConsults(direction string) ([]api.Consult, error) {
	f.dir = direction
	return f.out, f.err
}

// Inbound questions become messages the existing inbox can render: the asker is the "peer", the
// project comes along, and the ANSWERING DAEMON is carried so the modal can tell "answer it here"
// from "answer this on <device>".
func TestInboundPeerMessages(t *testing.T) {
	asked := time.Now().Add(-3 * time.Minute)
	f := &fakeConsults{out: []api.Consult{{
		ConsultID: "c1", Direction: dirInbound, Status: "pending", ProjectLabel: "partyline",
		Question: "does the limiter retry 429s?", Peer: "darcy", DaemonID: "d-mine",
		DeviceLabel: "air", CreatedAt: asked,
	}}}
	got := inboundPeerMessages(f)
	if f.dir != dirInbound {
		t.Errorf("asked the server for %q, want inbound", f.dir)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	m := got[0]
	if m.ID != "c1" || m.Direction != dirInbound || m.Peer != "darcy" || m.Project != "partyline" {
		t.Errorf("message = %+v", m)
	}
	if m.Daemon != "d-mine" || m.Device != "air" {
		t.Errorf("answering machine lost: %+v", m)
	}
	if m.Status != taskAuthRequired || m.Resolved() {
		t.Errorf("an unanswered question must read as waiting: %+v", m)
	}
	if !m.AskedAt.Equal(asked) {
		t.Error("AskedAt must carry the server's created_at — it's how long they've waited")
	}
}

// A control-plane hiccup must cost you nothing: the modal still opens on your own outbound messages.
func TestInboundPeerMessagesToleratesFailure(t *testing.T) {
	if got := inboundPeerMessages(&fakeConsults{err: errors.New("503")}); got != nil {
		t.Errorf("got %v on an error, want nil", got)
	}
	if got := inboundPeerMessages(nil); got != nil {
		t.Errorf("got %v with no client, want nil", got)
	}
}

// ONE list, both directions, newest first — the founder's ask is that inbound and outbound are the
// same conversation, not two UIs.
func TestMergePeerMessages(t *testing.T) {
	now := time.Now()
	local := []peerMessage{
		{ID: "out-new", Direction: dirOutbound, Peer: "mac-studio", Status: taskCompleted, AskedAt: now.Add(-time.Minute)},
		{ID: "out-old", Direction: dirOutbound, Peer: "mac-studio", Status: taskSubmitted, AskedAt: now.Add(-10 * time.Minute)},
	}
	inbound := []peerMessage{
		{ID: "in-mid", Direction: dirInbound, Peer: "darcy", Status: taskAuthRequired, AskedAt: now.Add(-5 * time.Minute)},
	}
	got := mergePeerMessages(local, inbound)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "out-new,in-mid,out-old" {
		t.Errorf("merged order = %v, want out-new,in-mid,out-old", ids)
	}
}

// The same consult can appear in both halves (I asked my OWN machine — a self-consult). One row, and
// the local record wins because only it can carry a resolved answer.
func TestMergePeerMessagesDedupes(t *testing.T) {
	now := time.Now()
	got := mergePeerMessages(
		[]peerMessage{{ID: "c1", Direction: dirOutbound, Status: taskCompleted, Answer: "yes", AskedAt: now}},
		[]peerMessage{{ID: "c1", Direction: dirInbound, Status: taskAuthRequired, AskedAt: now}},
	)
	if len(got) != 1 {
		t.Fatalf("got %d rows for one consult, want 1", len(got))
	}
	if got[0].Answer != "yes" || got[0].Direction != dirOutbound {
		t.Errorf("the local record must win: %+v", got[0])
	}
}

// An inbound row reads as a question asked OF you — the `darcy asks · partyline` shape — with how long
// they've been waiting where an outbound row shows "still out".
func TestPeerMessageRowRendersInbound(t *testing.T) {
	in := peerMessageRow(peerMessage{ID: "c1", Direction: dirInbound, Peer: "darcy",
		Project: "partyline", Question: "does the limiter retry 429s?", Status: taskAuthRequired,
		AskedAt: time.Now().Add(-4 * time.Minute)})
	for _, want := range []string{"darcy asks", "partyline", "waiting 4m", "limiter"} {
		if !strings.Contains(in, want) {
			t.Errorf("inbound row %q missing %q", in, want)
		}
	}
	out := peerMessageRow(peerMessage{ID: "c2", Direction: dirOutbound, Peer: "mac-studio",
		Project: "acr-cloud", Question: "safe to merge?", Status: taskSubmitted, AskedAt: time.Now()})
	if strings.Contains(out, "asks") {
		t.Errorf("an outbound row must not read as a question asked of you: %q", out)
	}
	if !strings.Contains(out, "still out") {
		t.Errorf("outbound row %q lost its state", out)
	}
}

// THE NOT-ON-THIS-MACHINE PATH. A consult is answerable only on the machine it was addressed to (its
// device token, its checkout). Anywhere else the modal must say where to go, not fail obscurely.
func TestInboundAnswerTarget(t *testing.T) {
	m := peerMessage{ID: "c1", Direction: dirInbound, Daemon: "d-air", Device: "air"}

	if ok, note := inboundAnswerTarget(m, "d-air"); !ok || note != "" {
		t.Errorf("on the right machine = (%v, %q), want (true, \"\")", ok, note)
	}

	ok, note := inboundAnswerTarget(m, "d-mac-studio")
	if ok {
		t.Error("a consult addressed to another machine must not be answerable here")
	}
	if !strings.Contains(note, "answer this on air") {
		t.Errorf("note = %q, want it to name the machine", note)
	}

	if ok, note := inboundAnswerTarget(m, ""); ok || !strings.Contains(note, "air") {
		t.Errorf("with no daemon enrolled = (%v, %q), want a pointer at air", ok, note)
	}

	// Degenerate: a record with no machine at all still gets a sentence, never a crash.
	if ok, note := inboundAnswerTarget(peerMessage{ID: "c1"}, "d-air"); ok || note == "" {
		t.Errorf("unaddressed = (%v, %q), want (false, a note)", ok, note)
	}
}

// "No daemon running" is the common case and is not a stack trace — it's an instruction.
func TestInboundFetchNote(t *testing.T) {
	if got := inboundFetchNote(daemonctl.ErrNoDaemon); !strings.Contains(got, "ptln daemon") {
		t.Errorf("note = %q, want it to say how to start the daemon", got)
	}
	if got := inboundFetchNote(errors.New("boom")); !strings.Contains(got, "boom") {
		t.Errorf("note = %q, want the cause carried through", got)
	}
}

func TestQuestionLinesWrapsAndBounds(t *testing.T) {
	long := strings.Repeat("word ", 400)
	got := questionLines(long, 40, 6)
	if len(got) != 7 { // 6 lines + the elision marker
		t.Fatalf("got %d lines, want 6 + an ellipsis", len(got))
	}
	if !strings.Contains(got[len(got)-1], "…") {
		t.Error("a clipped question must show it was clipped")
	}
	if got := questionLines("", 40, 6); len(got) != 1 || !strings.Contains(got[0], "empty") {
		t.Errorf("empty question = %v", got)
	}
}
