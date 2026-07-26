package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

type fakePoller struct {
	mu    sync.Mutex
	calls int
	res   []api.ConsultResult // consumed one per call; the last repeats
	err   error
}

func (f *fakePoller) GetConsult(string) (*api.ConsultResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	i := f.calls - 1
	if i >= len(f.res) {
		i = len(f.res) - 1
	}
	r := f.res[i]
	return &r, nil
}

func (f *fakePoller) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type fakeBanner struct {
	mu   sync.Mutex
	last string
}

func (b *fakeBanner) SetBanner(s string) { b.mu.Lock(); b.last = s; b.mu.Unlock() }
func (b *fakeBanner) get() string        { b.mu.Lock(); defer b.mu.Unlock(); return b.last }

func TestConsultTerminal(t *testing.T) {
	for _, s := range []string{"answered", "declined", "timed_out", "failed"} {
		if !consultTerminal(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []string{"", "pending", "delivered"} {
		if consultTerminal(s) {
			t.Errorf("%q should NOT be terminal", s)
		}
	}
}

// pollUntil is the bound on the watcher: it stops at its context deadline instead of looping
// forever. Without this the "answer arrives later" path would leak a goroutine per ask.
func TestPollUntilStopsAtItsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	calls := 0
	start := time.Now()
	if pollUntil(ctx, 5*time.Millisecond, func() bool { calls++; return false }) {
		t.Fatal("pollUntil reported resolved when fn never said true")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("pollUntil ran %v past its 60ms deadline", el)
	}
	if calls < 2 {
		t.Fatalf("calls = %d — it should have polled up front and then on the ticker", calls)
	}
}

// An already-expired context must not poll more than the one up-front call.
func TestPollUntilWithAnExpiredContext(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	calls := 0
	if pollUntil(ctx, time.Millisecond, func() bool { calls++; return false }) {
		t.Fatal("want not resolved")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want the single up-front poll", calls)
	}
}

func TestWatchPeerMessageFilesTheAnswerAndBanners(t *testing.T) {
	p := &fakePoller{res: []api.ConsultResult{{Status: "pending"}, {Status: "answered", Answer: "yes, with jitter"}}}
	b := &fakeBanner{}
	var got peerMessage
	m := peerMessage{ID: "c1", Direction: dirOutbound, Peer: "mac-studio", Project: "acr-cloud",
		Status: msgWaiting, AskedAt: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watchPeerMessage(ctx, p, m, time.Millisecond, func(m peerMessage) { got = m }, b)

	if got.Status != "answered" || got.Answer != "yes, with jitter" {
		t.Fatalf("stored %+v, want the answered result", got)
	}
	if got.AnsweredAt.IsZero() {
		t.Error("answered_at not stamped")
	}
	if got.Read {
		t.Error("a freshly-arrived answer must be UNREAD — that's what puts it in the inbox")
	}
	if s := b.get(); !strings.Contains(s, "mac-studio") || !strings.Contains(s, "ctrl-\\ p") {
		t.Fatalf("banner = %q, want the peer and the key that reads it", s)
	}
}

// The deadline path still FILES something: an ask that dies silently would otherwise sit in the
// inbox as "still out" forever.
func TestWatchPeerMessageRecordsATimeoutAtItsDeadline(t *testing.T) {
	p := &fakePoller{res: []api.ConsultResult{{Status: "pending"}}}
	b := &fakeBanner{}
	var got peerMessage
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	watchPeerMessage(ctx, p, peerMessage{ID: "c2", Peer: "air", Status: msgWaiting, AskedAt: time.Now()},
		5*time.Millisecond, func(m peerMessage) { got = m }, b)
	if got.Status != "timed_out" {
		t.Fatalf("status = %q, want timed_out", got.Status)
	}
	if !strings.Contains(b.get(), "never answered") {
		t.Fatalf("banner = %q", b.get())
	}
	if p.count() < 2 {
		t.Fatalf("polls = %d — the watcher should have retried before giving up", p.count())
	}
}

// A poll error is transient, not terminal: keep trying until the deadline decides.
func TestWatchPeerMessageTreatsPollErrorsAsTransient(t *testing.T) {
	p := &fakePoller{err: errors.New("no route to host")}
	var got peerMessage
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	watchPeerMessage(ctx, p, peerMessage{ID: "c3", Peer: "air", Status: msgWaiting, AskedAt: time.Now()},
		5*time.Millisecond, func(m peerMessage) { got = m }, nil)
	if got.Status != "timed_out" {
		t.Fatalf("status = %q, want timed_out after the deadline", got.Status)
	}
	if p.count() < 2 {
		t.Fatalf("polls = %d — an error must not end the watch", p.count())
	}
}

// A declined consult carries its reason in Detail, not Answer — the inbox must still show it.
func TestWatchPeerMessageCarriesTheDeclineDetail(t *testing.T) {
	p := &fakePoller{res: []api.ConsultResult{{Status: "declined", Detail: "that project moved"}}}
	var got peerMessage
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	watchPeerMessage(ctx, p, peerMessage{ID: "c4", Peer: "air", Status: msgWaiting, AskedAt: time.Now()},
		time.Millisecond, func(m peerMessage) { got = m }, nil)
	if got.Status != "declined" || got.Answer != "that project moved" {
		t.Fatalf("stored %+v, want the decline reason", got)
	}
}

func TestPeerBannerNamesTheStatus(t *testing.T) {
	for status, want := range map[string]string{
		"answered": "answered your question", "declined": "declined your question",
		"timed_out": "never answered", "failed": "couldn't answer", "weird": "replied",
	} {
		if s := peerBanner(peerMessage{Peer: "mac-studio", Status: status}); !strings.Contains(s, want) {
			t.Errorf("peerBanner(%q) = %q, want it to contain %q", status, s, want)
		}
	}
}
