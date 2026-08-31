package main

import (
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/ptysess"
)

// fakeMux stands in for the mux: no PTY, no terminal. Records what got pasted and where.
type fakeMux struct {
	liveKey     string
	dir         string
	unsubmitted int
	unknown     bool // UnsubmittedInput reports known=false (session not live here)
	status      string
	pastes      map[string]string // session key → the block handed to paste
	submitted   map[string]bool   // session key → whether paste actually pressed Enter
	// typedAfterPaste: input bytes the "human" adds during pasteSubmitGap, i.e. after the safety check
	// already passed and before the Enter goes out. Non-zero must abort the submit.
	typedAfterPaste int
	banner          string
}

func newFakeMux(key, dir string) *fakeMux {
	return &fakeMux{liveKey: key, dir: dir, status: "waiting",
		pastes: map[string]string{}, submitted: map[string]bool{}}
}

func (f *fakeMux) SessionByKey(key string) (sessIO, string, string, bool) {
	if key != "" && key == f.liveKey {
		// A nil *Session with ok=true would be indistinguishable from "not live" to the deliverer, so
		// hand back a non-nil zero value; nothing in the decision path dereferences it (paste is faked).
		return &ptysess.Session{}, "claude · " + f.liveKey, f.dir, true
	}
	return nil, "", "", false
}

func (f *fakeMux) UnsubmittedInput(key string) (int, bool) {
	if f.unknown || key != f.liveKey {
		return 0, false
	}
	return f.unsubmitted, true
}

func (f *fakeMux) SessionStatus(key string) string {
	if key != f.liveKey {
		return ""
	}
	return f.status
}

func (f *fakeMux) SetBanner(s string) { f.banner = s }

// paste mirrors pasteBlock's real contract: the Enter is a SECOND write after a gap, and confirm is
// re-consulted in that gap and can veto it. typedAfterPaste stands in for a human typing while the
// block lands — it is applied before confirm runs, so the veto is decided by the real unsafeToSubmit
// reading the real UnsubmittedInput, not by a flag the fake interprets itself.
func (f *fakeMux) paste(_ sessIO, block string, submit bool, confirm func() string) bool {
	f.pastes[f.liveKey] = block
	f.submitted[f.liveKey] = false
	if !submit {
		return false
	}
	f.unsubmitted += f.typedAfterPaste
	if confirm != nil {
		if why := confirm(); why != "" {
			return false
		}
	}
	f.submitted[f.liveKey] = true
	return true
}

func answeredMsg(session string) peerMessage {
	return peerMessage{ID: "c-1", Direction: dirOutbound, Peer: "mac-studio", Project: "web",
		Question: "does this break your callers?", Answer: "yes, the v1 shape is load-bearing",
		Status: taskCompleted, AskedAt: time.Now(), Session: session}
}

// registerProject makes dir a registered project with the given delivery policy.
func registerProject(t *testing.T, dir, deliver string) {
	t.Helper()
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "web", Path: dir, Deliver: deliver},
	}}); err != nil {
		t.Fatal(err)
	}
}

// STAGED, NOT SUBMITTED, IS THE DEFAULT. A project that has said nothing must not have a teammate's
// text submitted to its agent — and the giveaway is the trailing byte: no CR means it cannot become a
// turn without a human keystroke.
func TestDeliveryDefaultsToStagedNotSubmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	registerProject(t, dir, "") // nothing said
	mx := newFakeMux("s-1", dir)

	mode, banner := deliverToAskingSession(mx, answeredMsg("s-1"), mx.paste)
	if mode != deliverStage {
		t.Fatalf("mode = %v, want deliverStage", mode)
	}
	got := mx.pastes["s-1"]
	if got == "" {
		t.Fatal("nothing was staged — a banner alone is the behaviour this replaces")
	}
	if mx.submitted["s-1"] {
		t.Fatalf("staged block must not be submitted: %q", got)
	}
	if !strings.Contains(banner, "press ⏎") {
		t.Fatalf("the banner must tell them it needs a keystroke: %q", banner)
	}
}

// An unregistered directory has nobody's opt-in, so it stages too. "No setting" must not read as yes.
func TestDeliveryUnregisteredDirStages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mx := newFakeMux("s-1", t.TempDir()) // no registry at all
	if mode, _ := deliverToAskingSession(mx, answeredMsg("s-1"), mx.paste); mode != deliverStage {
		t.Fatalf("mode = %v, want deliverStage for a dir nobody opted in", mode)
	}
}

// Auto-submit fires ONLY on the explicit opt-in, and then the block is the staged block plus exactly
// one CR — nothing about the untrusted framing is simplified to save a line.
func TestDeliverySubmitsOnlyOnOptIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	registerProject(t, dir, "submit")
	mx := newFakeMux("s-1", dir)
	mx.status, mx.unsubmitted = "waiting", 0

	mode, _ := deliverToAskingSession(mx, answeredMsg("s-1"), mx.paste)
	if mode != deliverSubmit {
		t.Fatalf("mode = %v, want deliverSubmit", mode)
	}
	submitted := mx.pastes["s-1"]
	if !mx.submitted["s-1"] {
		t.Fatal("the opt-in path must actually press Enter")
	}

	// Byte-identical framing on both paths, CR aside. The labelled fence is what makes the agent treat
	// the answer as data; the unattended path is the one that needs it most.
	registerProject(t, dir, "stage")
	mx2 := newFakeMux("s-1", dir)
	deliverToAskingSession(mx2, answeredMsg("s-1"), mx2.paste)
	staged := mx2.pastes["s-1"]
	if submitted != staged {
		t.Fatalf("framing must be byte-identical:\n submit: %q\n stage:  %q", submitted, staged)
	}
	if mx2.submitted["s-1"] {
		t.Fatal("the stage path must not submit")
	}
	if !strings.Contains(staged, "untrusted") || !strings.Contains(staged, "peer feedback from") {
		t.Fatalf("the untrusted fence is missing: %q", staged)
	}
}

// DELIVERY TARGETS ONLY THE SESSION THAT ASKED. Not the focused one, not all of them, and NEVER a
// fallback when the origin is unknown — that would put a teammate's text into whichever agent the
// human happened to be looking at.
func TestDeliveryTargetsOnlyTheOriginatingSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	registerProject(t, dir, "submit") // even fully opted in

	// The ask came from s-1; only s-2 is live.
	mx := newFakeMux("s-2", dir)
	mode, banner := deliverToAskingSession(mx, answeredMsg("s-1"), mx.paste)
	if len(mx.pastes) != 0 {
		t.Fatalf("pasted into a session that did not ask: %v", mx.pastes)
	}
	if mode != deliverBanner {
		t.Fatalf("mode = %v, want deliverBanner when the asking session is gone", mode)
	}
	if !strings.Contains(banner, "ctrl-\\ p") {
		t.Fatalf("the banner must say where the answer is: %q", banner)
	}

	// No recorded origin at all (an ask from outside any mux) must not fall back to anything.
	mx2 := newFakeMux("s-2", dir)
	if mode, _ := deliverToAskingSession(mx2, answeredMsg(""), mx2.paste); mode != deliverBanner {
		t.Fatalf("mode = %v, want deliverBanner with no recorded session", mode)
	}
	if len(mx2.pastes) != 0 {
		t.Fatalf("an origin-less answer was injected somewhere: %v", mx2.pastes)
	}
}

// Never inject mid-turn, and never into a half-typed prompt. Opted in or not, an observable hazard
// degrades to staged — which is safe because it needs a keystroke.
// TestSubmitAbortsIfTypedWhileTheBlockLands covers the window the real fix opened. Submitting is two
// writes with a gap between them (pasteSubmitGap — claude absorbs a CR that shares a read() with the
// closing fence, so it has to be its own write). unsafeToSubmit passes BEFORE the paste; if a human
// starts typing during the gap, their half-line would be submitted along with the peer's text — the
// exact hazard the check exists to prevent, just relocated. So confirm re-runs inside the gap.
//
// It must also degrade honestly: the banner has to say staged, because that's what happened.
func TestSubmitAbortsIfTypedWhileTheBlockLands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	registerProject(t, dir, "submit")

	mx := newFakeMux("s-1", dir)
	mx.typedAfterPaste = 5 // clean at the check, dirty by the time the Enter would go out

	mode, banner := deliverToAskingSession(mx, answeredMsg("s-1"), mx.paste)
	if mode != deliverStage {
		t.Fatalf("mode = %v, want deliverStage — a submit raced a human keystroke", mode)
	}
	if mx.submitted["s-1"] {
		t.Error("pressed Enter after the human started typing")
	}
	if mx.pastes["s-1"] == "" {
		t.Error("the answer should still be staged, not dropped")
	}
	if !strings.Contains(banner, "press ⏎") {
		t.Errorf("banner must tell the human it's staged and needs a keystroke: %q", banner)
	}
	if strings.Contains(banner, "delivered") {
		t.Errorf("banner claims delivery that did not happen: %q", banner)
	}
}

func TestDeliveryDegradesToStagedWhenSubmitIsUnsafe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	registerProject(t, dir, "submit")

	cases := []struct {
		name  string
		setup func(*fakeMux)
		want  string
	}{
		{"human has typed something unsent", func(f *fakeMux) { f.unsubmitted = 7 }, "typed"},
		{"agent is mid-turn", func(f *fakeMux) { f.status = "active" }, "still working"},
		{"engine state unknown", func(f *fakeMux) { f.status = "" }, "still working"},
		{"session not tracked", func(f *fakeMux) { f.unknown = true }, "can't tell"},
	}
	for _, c := range cases {
		mx := newFakeMux("s-1", dir)
		c.setup(mx)
		mode, banner := deliverToAskingSession(mx, answeredMsg("s-1"), mx.paste)
		if mode != deliverStage {
			t.Errorf("%s: mode = %v, want deliverStage", c.name, mode)
		}
		if mx.submitted["s-1"] {
			t.Errorf("%s: submitted anyway", c.name)
		}
		if !strings.Contains(banner, c.want) {
			t.Errorf("%s: banner should explain why (%q): %q", c.name, c.want, banner)
		}
	}
}

// A decline, a timeout or a failure has no peer text to hand over — banner only, nothing injected.
func TestNonAnswersAreNeverInjected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	registerProject(t, dir, "submit")
	for _, st := range []string{taskRejected, taskFailed, taskCanceled} {
		mx := newFakeMux("s-1", dir)
		m := answeredMsg("s-1")
		m.Status, m.Answer = st, ""
		if mode, _ := deliverToAskingSession(mx, m, mx.paste); mode != deliverBanner {
			t.Errorf("%s: mode = %v, want deliverBanner", st, mode)
		}
		if len(mx.pastes) != 0 {
			t.Errorf("%s: injected a non-answer: %v", st, mx.pastes)
		}
	}
}

// The delivery policy is a THIRD privilege: it must not be readable off either of the other two.
func TestDeliverPolicyIsIndependentOfTheOtherTwo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	// Full Auto launch + auto-answer consults, but nothing said about delivery.
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "web", Path: dir, Policy: "auto", Consults: "auto"},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := consultDeliverPolicy(dir); got != deliverStage {
		t.Fatalf("a full-Auto, auto-answering project must still STAGE by default, got %v", got)
	}
	// And opting into submit must not CHANGE either of the others. Launch defaults to auto
	// (registration is the consent), so the independence check is that an explicit ask SURVIVES
	// a deliver opt-in — not that launch happens to be gated.
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "web", Path: dir, Deliver: "submit", Policy: "ask"},
	}}); err != nil {
		t.Fatal(err)
	}
	p := loadDaemonRegistry().Projects[0]
	if p.launchPolicy() != "ask" {
		t.Fatal("deliver=submit must not override an explicit launch policy")
	}
	if consultDeliverPolicy(dir) != deliverSubmit {
		t.Fatal("the opt-in did not take effect")
	}
}
