//go:build realpty

// peer_deliver_realpty_manual_test.go — the one assumption in peer_deliver.go that a fake mux cannot
// check: that a CR written AFTER the closing \x1b[201~ is read by a real engine as a KEYPRESS and
// submits the turn, while the same bytes without that CR only stage text in the composer.
//
// WHY IT IS BUILD-TAGGED. This spawns real coding agents in real PTYs and (for the submit case) burns
// a real, if tiny, turn. It cannot run in CI, it needs the engines installed and authenticated, and it
// is slow. `go test ./...` must never pick it up — run it deliberately:
//
//	go test -tags realpty -run TestRealPTYPasteSubmitContract -v -timeout 900s .
//	PTLN_REALPTY_ENGINES=codex go test -tags realpty -run TestRealPTYPasteSubmitContract -v -timeout 300s .
//	PTLN_REALPTY_REPS=5 ...      repeat the submit case, because the outcome turned out to be a RACE
//	PTLN_REALPTY_SPLITCR=1 ...   send the CR as its own write after a settling gap (the candidate fix)
//	PTLN_REALPTY_OBSERVE=1 ...   dump the screen every 2s instead of trusting the detectors
//
// WHAT IT ASSERTS, PER ENGINE:
//
//	submit=false  the block with NO trailing CR starts NOTHING. This is the safety-critical half: if a
//	              bare paste ever submits, deliverStage is a lie and its default is unsafe. Run first,
//	              so a failure here costs no tokens.
//	submit=true   the same block plus a trailing CR STARTS A TURN, per the engine's own output.
//
// It deliberately calls pasteBlock() and peerAnswerBlock() themselves rather than a copy of the bytes,
// so the thing under test is the shipping write path.
//
// WHAT IT FOUND, 2026-07-25, macOS 15, engines as installed:
//
//	engine    no-CR stages, does not submit   CR appended to the paste write submits
//	claude    yes (25s, 1 run)                2 of 5 runs — A RACE, not a contract
//	codex     yes (25s, 1 run)                3 of 3 runs
//	opencode  yes (25s, 1 run)                3 of 3 runs
//	gemini    NOT TESTED — cannot authenticate: "This client is no longer supported for Gemini Code
//	          Assist for individuals", so it never reaches a composer.
//	goose     NOT TESTED — unconfigured first run: it opens `goose configure` (telemetry consent, then
//	          provider + API key) and never reaches a composer without interactive setup.
//
// THE CLAUDE RESULT IS THE POINT. The CR is outside the fence, as pasteBlock's own comment insists it
// must be — and it STILL does not reliably submit, because being outside the fence is not sufficient.
// Claude's TUI reads stdin in chunks: when the closing \x1b[201~ and the CR arrive in the SAME read,
// the CR is absorbed into the paste; when the child's read() happens to split them, it is a keypress.
// Same bytes, same code, either outcome. The old trap failed safe and silently; this one fails safe,
// silently, AND intermittently, which is worse — deliverSubmit appears to work whenever you check it.
//
// With PTLN_REALPTY_SPLITCR=1 (paste, 750ms, then the CR as its own write) claude submitted 5 of 5.
package main

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/ptysess"
)

// realPTYMarker is the distinctive text the screen is searched for. Spelled so it cannot collide with
// any engine's own chrome.
const realPTYMarker = "PTLNPASTE7391"

// realEngine is one engine's PTY contract: how to launch it, how to know it is ready for input, and
// how to know from its own output that a turn actually started.
type realEngine struct {
	name string
	argv []string
	// ready reports that the composer is accepting input. NEVER a fixed sleep: to a fixed sleep,
	// "not ready yet" and "swallowed the paste" look identical, and the whole result becomes a guess.
	ready func(scr string) bool
	// turnStarted reports, from the engine's own rendering, that the pasted text left the composer and
	// became a turn — a spinner, an interrupt hint, a reply, or the composer visibly emptying.
	turnStarted func(scr, marker string) bool
	// note is anything a reader of the results needs to know about this engine.
	note string
}

func has(scr string, subs ...string) bool {
	for _, s := range subs {
		if strings.Contains(scr, s) {
			return true
		}
	}
	return false
}

func realEngines() []realEngine {
	return []realEngine{{
		name: "claude",
		argv: []string{"claude"},
		ready: func(scr string) bool {
			// The composer prompt, once the folder-trust gate and the startup hooks are done with.
			return has(scr, "for more") && strings.Contains(scr, "\n❯") && !has(scr, "I trust this folder")
		},
		turnStarted: func(scr, marker string) bool {
			// "⏺" is claude's assistant/tool bullet: it cannot be on screen before a turn has been sent
			// (startup hook output uses "⎿" and "⚠"), and it is there whether the model answers or refuses.
			// The interrupt hint is the in-flight version of the same fact.
			return has(scr, "⏺", "esc to interrupt")
		},
	}, {
		name: "codex",
		argv: []string{"codex"},
		ready: func(scr string) bool {
			return has(scr, "/model to change") && has(scr, "Implement {feature}", "\n› ")
		},
		turnStarted: func(scr, marker string) bool {
			return has(scr, "Esc to interrupt", "esc to interrupt", "Working", "Thinking", "thinking")
		},
	}, {
		name:  "opencode",
		argv:  []string{"opencode"},
		ready: func(scr string) bool { return has(scr, "Ask anything") },
		turnStarted: func(scr, marker string) bool {
			// While STAGED, opencode shows the paste collapsed as "[Pasted ~N lines]" and the marker is
			// nowhere on screen. Once SENT, the placeholder is gone and the text is rendered verbatim as a
			// user message — so those two facts together are the discriminator, and "Thought:" (the model
			// starting work) confirms it.
			return has(scr, "Thought:") || (strings.Contains(scr, marker) && !has(scr, "[Pasted"))
		},
		note: "collapses a multi-line paste to \"[Pasted ~N lines]\" while staged — a human looking at the " +
			"session cannot read the answer until it is sent",
	}}
}

var realPTYAnsi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\a]*\a|\x1b[()][A-Za-z0-9]`)

// realPTYScreen is the session's own server-side screen model rendered as plain text. Reading the
// engine's state from the same vt the mux uses is deliberate: this is the view the delivery code has.
func realPTYScreen(s *ptysess.Session) string {
	return realPTYAnsi.ReplaceAllString(string(s.Snapshot()), "")
}

// realPTYDismiss answers the first-run gates that stand between a fresh PTY and a live composer
// (folder trust, update nags). They are not what is under test — but they matter to the delivery code
// all the same: a mux-launched engine in a directory it has not seen before sits behind one of these,
// and a paste delivered then goes into a MODAL, not a composer.
func realPTYDismiss(s *ptysess.Session, scr string) string {
	switch {
	case has(scr, "I trust this folder"):
		s.WriteInput([]byte("\r"))
		return "claude folder-trust"
	case has(scr, "1. Yes, continue"):
		s.WriteInput([]byte("\r"))
		return "codex directory-trust"
	case has(scr, "Skip until next version"):
		s.WriteInput([]byte("\x1b[B\r")) // move off "Update now", take "Skip"
		return "codex update-nag"
	}
	return ""
}

// realPTYWaitReady polls the screen for the engine's own readiness marker, dismissing first-run gates
// as they appear, and additionally waits for the screen to STOP CHANGING. Readiness alone is not
// enough: these TUIs paint a composer while startup hooks and MCP handshakes are still running, and a
// keystroke delivered into that window can be dropped — which would look exactly like "the engine
// ignores CR", i.e. it would fake the very finding this test exists to establish.
func realPTYWaitReady(t *testing.T, e realEngine, s *ptysess.Session, budget time.Duration) {
	t.Helper()
	const settle = 3 * time.Second
	start := time.Now()
	var last string
	var readySince, sameSince time.Time
	for time.Since(start) < budget {
		scr := realPTYScreen(s)
		if scr != last {
			sameSince, last = time.Now(), scr
		}
		switch {
		case !e.ready(scr):
			readySince = time.Time{}
			if what := realPTYDismiss(s, scr); what != "" {
				t.Logf("[%s] dismissed %s", e.name, what)
			}
		case readySince.IsZero():
			readySince = time.Now()
		case time.Since(readySince) >= settle && time.Since(sameSince) >= settle:
			t.Logf("[%s] ready and quiet after %s", e.name, time.Since(start).Round(time.Second))
			return
		}
		select {
		case <-s.Done:
			t.Fatalf("[%s] exited before it was ready; last screen:\n%s", e.name, last)
		case <-time.After(400 * time.Millisecond):
		}
	}
	t.Fatalf("[%s] never became ready within %s — the paste contract cannot be tested against an engine "+
		"that is not accepting input. Last screen:\n%s", e.name, budget, last)
}

// realPTYWatch polls for a started turn until the budget runs out, returning the last screen.
func realPTYWatch(t *testing.T, e realEngine, s *ptysess.Session, marker string, budget time.Duration) (bool, string) {
	observe := os.Getenv("PTLN_REALPTY_OBSERVE") != ""
	start := time.Now()
	var last string
	for time.Since(start) < budget {
		last = realPTYScreen(s)
		if e.turnStarted(last, marker) && !observe {
			return true, last
		}
		if observe {
			t.Logf("[%s] t=%s started=%v\n%s", e.name, time.Since(start).Round(time.Second),
				e.turnStarted(last, marker), last)
			time.Sleep(2 * time.Second)
			continue
		}
		time.Sleep(400 * time.Millisecond)
	}
	return e.turnStarted(last, marker), last
}

func TestRealPTYPasteSubmitContract(t *testing.T) {
	want := os.Getenv("PTLN_REALPTY_ENGINES")
	for _, e := range realEngines() {
		if want != "" && !has(","+want+",", ","+e.name+",") {
			continue
		}
		e := e
		t.Run(e.name, func(t *testing.T) {
			if e.note != "" {
				t.Logf("[%s] note: %s", e.name, e.note)
			}
			// The control case FIRST: if a bare paste submits, that is the serious finding and there is no
			// point spending a turn on the other half.
			if ctrl := realPTYCase(t, e, false); ctrl.inline {
				t.Errorf("SERIOUS: %s SUBMITTED a bracketed paste with NO trailing CR — deliverStage is "+
					"not safe on this engine", e.name)
			} else {
				t.Logf("[%s] PASS control: paste with no CR did not submit", e.name)
			}
			// REPEATED, because the answer turned out not to be a constant. Whether the trailing CR is
			// seen as a keypress depends on how the child's read() happens to split the write, so a single
			// run can report either outcome and "it worked once" is not a contract.
			reps := realPTYReps()
			var inline, late int
			for i := 0; i < reps; i++ {
				switch r := realPTYCase(t, e, true); {
				case r.inline:
					inline++
				case r.lateCR:
					late++
				}
			}
			t.Logf("[%s] submit tally over %d run(s): inline_cr_submitted=%d delayed_standalone_cr_submitted=%d "+
				"never_submitted=%d", e.name, reps, inline, late, reps-inline-late)
			switch {
			case inline == reps:
				t.Logf("[%s] PASS: a CR in the SAME write as the closing \\x1b[201~ submitted the turn, "+
					"%d/%d runs", e.name, inline, reps)
			case inline > 0:
				t.Errorf("RACE: %s submitted on the inline CR in only %d of %d runs. pasteBlock's single "+
					"write is nondeterministic here — the CR is a keypress only when the child's read() "+
					"happens to split it away from the paste. deliverSubmit silently stages instead of "+
					"submitting the rest of the time", e.name, inline, reps)
			case late > 0:
				t.Errorf("%s did NOT submit on a CR appended to the paste write (0/%d) but DID submit on the "+
					"same CR sent %s later as its own write (%d/%d). pasteBlock's single write is a no-op "+
					"here: the Enter has to be separated from the paste", e.name, reps, realPTYLateCRDelay,
					late, reps)
			default:
				t.Errorf("%s did NOT submit on CR after \\x1b[201~, inline OR delayed — Enter is not this "+
					"engine's submit key from a paste, and deliverSubmit is a silent no-op", e.name)
			}
		})
	}
}

// realPTYLateCRDelay is how long the diagnostic second CR waits after the paste. Chosen to be clearly
// longer than any input-coalescing window an Ink/ratatui TUI could plausibly use.
const realPTYLateCRDelay = 750 * time.Millisecond

// realPTYReps is how many times the submit case runs. More than one because the outcome proved to be a
// race; each repetition that actually submits costs one one-word turn.
func realPTYReps() int {
	n, err := strconv.Atoi(os.Getenv("PTLN_REALPTY_REPS"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// realPTYResult separates the two ways "Enter" can reach the engine, because they are what tells a
// silent no-op apart from a fixable ordering bug:
//
//	inline  the CR rode along in the same write as the closing \x1b[201~ — exactly what pasteBlock does.
//	lateCR  the CR was sent afterwards as its own write, once the paste had settled.
type realPTYResult struct{ inline, lateCR bool }

// realPTYCase runs one engine, one submit setting, and reports how (if at all) the turn started. The
// engine is killed the moment the answer is known, so a submitted turn costs one short reply and
// nothing more.
func realPTYCase(t *testing.T, e realEngine, submit bool) realPTYResult {
	t.Helper()
	env := append(os.Environ(), "PARTYLINE=1", "TERM=xterm-256color")
	sess, err := ptysess.NewIn(t.TempDir(), e.argv, "realpty", false, env)
	if err != nil {
		t.Fatalf("[%s] launch: %v", e.name, err)
	}
	defer sess.End()
	sess.Attach("realpty", io.Discard, 100, 34, true, true)

	realPTYWaitReady(t, e, sess, 150*time.Second)

	marker := realPTYMarker + "-NOCR"
	if submit {
		marker = realPTYMarker + "-CR"
	}
	block := peerAnswerBlock("harness", "paste-submit-contract",
		"reply with the single word "+marker+" and do nothing else")

	// THE THING UNDER TEST: the shipping function, the shipping bytes. With PTLN_REALPTY_SPLITCR set,
	// the CANDIDATE FIX is exercised instead — the identical bytes, but with the CR as its own write once
	// the paste has been drained — so the proposed change can be measured rather than argued.
	// The split-CR fix now lives IN pasteBlock (see pasteSubmitGap), so the default path measures the
	// shipping code. PTLN_REALPTY_INLINECR=1 reproduces the OLD behaviour — CR appended to the paste
	// write — which is what measured 2/5 on claude. Keep it: it is the evidence that the gap is load
	// bearing, and the check that would catch someone "simplifying" pasteBlock back into one write.
	inline := submit && os.Getenv("PTLN_REALPTY_INLINECR") != ""
	if inline {
		t.Logf("[%s] reproducing the OLD inline-CR behaviour — expect intermittent submits", e.name)
		sess.WriteInput([]byte("\x1b[200~" + block + "\x1b[201~\r"))
	} else {
		// confirm=nil: no human is typing in a harness, and a veto here would be indistinguishable
		// from the race we are measuring.
		pasteBlock(sess, block, submit, nil)
	}

	// A submit shows itself in seconds. The control case is given a comparable window, or "it did not
	// submit" would just mean "we did not wait".
	budget := 25 * time.Second
	if submit {
		budget = 40 * time.Second
	}
	submitted, scr := realPTYWatch(t, e, sess, marker, budget)
	t.Logf("[%s submit=%v] inline_cr_started_turn=%v marker_verbatim_on_screen=%v fence_bytes_leaked=%v\nscreen:\n%s",
		e.name, submit, submitted, strings.Contains(scr, marker), has(scr, "[200~", "[201~"), scr)
	// The late-CR diagnosis only applies when we deliberately reproduced the OLD inline write and it
	// failed. On the default path pasteBlock already sent the CR as its own write, so a non-submit there
	// is a real failure of the shipping code, not something to re-probe.
	if submitted || !submit || !inline {
		return realPTYResult{inline: submitted}
	}
	// THE DIAGNOSIS. The inline CR did nothing. Send the SAME single byte again, this time as its own
	// write after the paste has settled. If that submits, the engine is not ignoring Enter — pasteBlock
	// is just handing it over in a write the engine folds into the paste, and the fix is an ordering one.
	time.Sleep(realPTYLateCRDelay)
	sess.WriteInput([]byte("\r"))
	late, scr := realPTYWatch(t, e, sess, marker, 45*time.Second)
	t.Logf("[%s submit=true] delayed_standalone_cr_started_turn=%v\nscreen:\n%s", e.name, late, scr)
	return realPTYResult{lateCR: late}
}
