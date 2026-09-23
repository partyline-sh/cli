package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real shape: a commit message with a subject, a body, a trailer, and the Co-Authored-By
// block that every agent-written commit in this repo carries under it. The trailer must survive
// having other trailers beneath it.
func TestATrailerIsFoundUnderOtherTrailers(t *testing.T) {
	msg := `an upgrade must reload the edge it just reconfigured

` + "`compose up -d`" + ` leaves a container alone when its service definition
has not changed.

Ptln-Gotcha: A rewritten Caddyfile is not served until caddy is restarted,
 because the file is a mount and compose sees no change to the service.

Co-Authored-By: Claude <noreply@anthropic.com>
Signed-off-by: Darcy <me@example.com>
`
	got := commitTrailers(msg)
	if len(got) != 1 {
		t.Fatalf("got %d trailers, want 1: %+v", len(got), got)
	}
	if got[0].Kind != "gotcha" {
		t.Errorf("kind = %q, want gotcha", got[0].Kind)
	}
	// The indented second line is a continuation, so a fact can be a sentence.
	if !strings.Contains(got[0].Body, "because the file is a mount") {
		t.Errorf("continuation line was dropped: %q", got[0].Body)
	}
	if strings.Contains(got[0].Body, "Co-Authored-By") {
		t.Errorf("the trailer swallowed the block under it: %q", got[0].Body)
	}
}

// Ordinary commits are the overwhelming majority, and every one of them must produce nothing.
// This is the property the whole design rests on: the brief is capped at 12, so a harvester that
// guesses fills it with sediment and people stop reading it.
func TestOrdinaryCommitsYieldNothing(t *testing.T) {
	for _, msg := range []string{
		"wip",
		"fix typo",
		"bump deps\n\nRenovate: update all the things\n",
		"Merge pull request #1384 from partyline-sh/cut/web-dead-ui",
		"refactor: move the describe form\n\nIt was under /work, which is gone.\n",
	} {
		if got := commitTrailers(msg); len(got) != 0 {
			t.Errorf("%q produced %+v; ordinary commits must produce nothing", msg, got)
		}
	}
}

func TestSeveralTrailersInOneCommit(t *testing.T) {
	msg := `rework the transport

Ptln-Decision: Fleet talks to integration over gRPC, not the POS links.
Ptln-Constraint: Anything downstream of the POS callback has to be idempotent.
`
	got := commitTrailers(msg)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %+v", len(got), got)
	}
	if got[0].Kind != "decision" || got[1].Kind != "constraint" {
		t.Errorf("kinds = %q, %q", got[0].Kind, got[1].Kind)
	}
}

// A misspelled kind is a typo, not a new category. Accepting it would let the closed set of five
// kinds drift open one commit at a time, and the set being closed is what keeps a brief scannable.
func TestAnUnknownKindIsIgnored(t *testing.T) {
	if got := commitTrailers("x\n\nPtln-Learning: something\nPtln-Note: another\n"); len(got) != 0 {
		t.Fatalf("unknown kinds were accepted: %+v", got)
	}
}

func TestAnEmptyTrailerIsNotAFact(t *testing.T) {
	if got := commitTrailers("x\n\nPtln-Gotcha:\n"); len(got) != 0 {
		t.Fatalf("an empty trailer became a fact: %+v", got)
	}
}

// Two people with the same repo cloned both harvest it. The ids must match, so git sees one file
// written twice rather than two facts — otherwise every shared repo double-records every fact.
func TestTheSameCommitAlwaysProducesTheSameFact(t *testing.T) {
	at := time.Date(2026, 9, 17, 22, 39, 17, 0, time.UTC)
	c := commitInfo{SHA: "4a50eb9c1d2f", Author: "matt", At: at,
		Message: "fix retries\n\nPtln-Gotcha: The POS callback retries 3x with no jitter.\n"}

	a := factsFromCommit(c, "integration-svc")
	b := factsFromCommit(c, "integration-svc")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one fact each, got %d and %d", len(a), len(b))
	}
	if a[0].ID != b[0].ID {
		t.Fatalf("ids differ across runs (%s vs %s) — the same commit would be recorded twice", a[0].ID, b[0].ID)
	}
	if a[0].Commit != c.SHA {
		t.Errorf("fact does not link back to its commit: %q", a[0].Commit)
	}
	if a[0].By != "matt" || a[0].Repo != "integration-svc" {
		t.Errorf("author/scope not carried: by=%q repo=%q", a[0].By, a[0].Repo)
	}

	// Two trailers in one commit must not collide with each other.
	c.Message = "x\n\nPtln-Gotcha: one\nPtln-Decision: two\n"
	two := factsFromCommit(c, "r")
	if len(two) != 2 || two[0].ID == two[1].ID {
		t.Fatalf("two trailers in one commit collided: %+v", two)
	}
}

// End to end over a real repo: git's own output is where a message with blank lines, colons or
// unicode gets mangled, and no amount of parser unit-testing catches that.
func TestHarvestReadsRealCommits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	harvestGit(t, repo, "init", "--quiet")
	harvestGit(t, repo, "config", "user.email", "matt@example.com")
	harvestGit(t, repo, "config", "user.name", "Matt")

	harvestCommit(t, repo, "a.txt", "1", "wip")
	harvestCommit(t, repo, "b.txt", "2", `handle the POS callback

It retries without jitter, which we found the hard way: two bookings
for one scan.

Ptln-Gotcha: The POS callback retries 3x with no jitter, so anything
 downstream has to be idempotent.

Co-Authored-By: Claude <noreply@anthropic.com>`)
	harvestCommit(t, repo, "c.txt", "3", "fix typo")

	got, err := readCommits(repo, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d commits, want 3", len(got))
	}
	// --reverse: oldest first, so the marker can be the last element.
	if !strings.HasPrefix(got[0].Message, "wip") {
		t.Errorf("commits are not oldest-first: %q", got[0].Message)
	}
	if got[1].Author != "Matt" {
		t.Errorf("author = %q, want Matt", got[1].Author)
	}

	var facts []fact
	for _, c := range got {
		facts = append(facts, factsFromCommit(c, "integration-svc")...)
	}
	if len(facts) != 1 {
		t.Fatalf("got %d facts from 3 commits, want 1: %+v", len(facts), facts)
	}
	if facts[0].Kind != "gotcha" || !strings.Contains(facts[0].Body, "idempotent") {
		t.Errorf("wrong fact: %+v", facts[0])
	}
	if strings.Contains(facts[0].Body, "Co-Authored-By") {
		t.Errorf("trailer swallowed the block under it: %q", facts[0].Body)
	}

	// And it round-trips through the store, so what is written is what is read back.
	ws := &projectWorkspace{Label: "acr", Dir: t.TempDir()}
	if _, err := writeFact(ws.Dir, facts[0]); err != nil {
		t.Fatal(err)
	}
	back, err := readFacts(ws.Dir, false)
	if err != nil || len(back) != 1 {
		t.Fatalf("readFacts = %v, %v", back, err)
	}
	if back[0].Commit != facts[0].Commit || back[0].Commit == "" {
		t.Errorf("the commit link did not survive the round trip: %q", back[0].Commit)
	}
}

func harvestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitMem(dir, args...); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func harvestCommit(t *testing.T, dir, file, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	harvestGit(t, dir, "add", file)
	harvestGit(t, dir, "commit", "--quiet", "-m", msg)
}

// A proposed fact is inferred, not witnessed. It must say so, cite what it came from, and sort
// under the facts a person chose to record — otherwise the weakest entry sets the credibility of
// the whole brief.
func TestAProposedFactIsMarkedCitedAndRankedLower(t *testing.T) {
	authored := fact{ID: "a", Kind: "decision", Repo: "r", By: "darcy", At: time.Now(), Body: "written by a person"}
	proposed := fact{ID: "b", Kind: "decision", Repo: "r", By: proposedBy, At: time.Now(),
		Sources: []string{"odoo:ACR-1412"}, Body: "mined from a ticket"}

	got := rankFacts([]fact{proposed, authored}, "r")
	if got[0].ID != "a" {
		t.Fatalf("proposed fact outranked an authored one: %s first", got[0].ID)
	}

	// Citations must survive the round trip, and must NOT land in tags — conflict detection keys
	// on shared tags, so two proposals citing one ticket would read as a disagreement about it.
	dir := t.TempDir()
	if _, err := writeFact(dir, proposed); err != nil {
		t.Fatal(err)
	}
	back, err := readFacts(dir, true)
	if err != nil || len(back) != 1 {
		t.Fatalf("readFacts = %v, %v", back, err)
	}
	if len(back[0].Sources) != 1 || back[0].Sources[0] != "odoo:ACR-1412" {
		t.Errorf("citations lost: %+v", back[0].Sources)
	}
	if len(back[0].Tags) != 0 {
		t.Errorf("citations leaked into tags (%v), which would fake a conflict", back[0].Tags)
	}
}

func TestProposalsDedupeAgainstWhatIsAlreadyKnown(t *testing.T) {
	existing := []fact{{ID: "a", Body: "The POS callback retries 3x   with no jitter"}}
	if _, found := dedupeAgainst(existing, "the pos callback RETRIES 3x with no jitter"); !found {
		t.Error("whitespace and case differences should not count as a new fact")
	}
	if _, found := dedupeAgainst(existing, "The POS callback retries twice"); found {
		t.Error("a genuinely different fact was swallowed as a duplicate")
	}
}

// A citation has to name a system and an id. "see the ticket" is not checkable, and an
// uncheckable proposal is the thing this mechanism exists to prevent.
func TestACitationMustNameASystemAndAnID(t *testing.T) {
	for _, bad := range []string{"", "ticket", ":123", "odoo:", "see odoo:123"} {
		if validSourceRef(bad) {
			t.Errorf("%q was accepted as a citation", bad)
		}
	}
	for _, good := range []string{"odoo:ACR-1412", "commit:4a50eb9", "claude-mem:53485"} {
		if !validSourceRef(good) {
			t.Errorf("%q was rejected", good)
		}
	}
}

// The distiller is asked to prefer saying nothing, and everything downstream depends on it
// actually doing so. These are the replies that must produce no facts.
func TestCaptureRecordsNothingFromAnOrdinarySession(t *testing.T) {
	for _, reply := range []string{
		`[]`,
		"I reviewed the session and found nothing durable.\n\n[]",
		"```json\n[]\n```",
		"",
		"not json at all",
		`[{"kind":"progress","body":"refactored the form"}]`, // not a kind
		`[{"kind":"decision","body":""}]`,                    // no body
	} {
		if got := parseCaptured(reply); len(got) != 0 {
			t.Errorf("reply %q produced %+v; must produce nothing", reply, got)
		}
	}
}

func TestCaptureTakesFactsAndEnforcesTheCap(t *testing.T) {
	got := parseCaptured("Here is what I found:\n```json\n" + `[
	  {"kind":"gotcha","body":"The POS callback retries 3x with no jitter."},
	  {"kind":"decision","body":"Fleet talks to integration over gRPC."},
	  {"kind":"constraint","body":"A third thing that must be dropped."}
	]` + "\n```")
	if len(got) != captureMax {
		t.Fatalf("got %d facts, want the cap of %d: %+v", len(got), captureMax, got)
	}
	if got[0].Kind != "gotcha" || !strings.Contains(got[1].Body, "gRPC") {
		t.Errorf("wrong facts: %+v", got)
	}
}

// A session Stops many times an hour. Distilling on every one would bill continuously and mostly
// re-read the same work.
func TestCaptureIsThrottledPerSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if !captureDue("sess-1") {
		t.Fatal("the first stop of a session should capture")
	}
	markCaptured("sess-1")
	if captureDue("sess-1") {
		t.Error("a second stop moments later must not capture again")
	}
	if !captureDue("sess-2") {
		t.Error("a different session has its own throttle")
	}
}

// Capture must not disable keep-going, and keep-going must not disable capture: a long
// autonomous session is exactly the one that learns the most.
func TestCaptureAndKeepGoingBothSurviveOnStop(t *testing.T) {
	out := sessionSettings(map[string]any{
		"Stop": []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": "ptln keepgoing-hook --key k"}},
		}},
	})
	if !strings.Contains(out, "memory-capture-hook") {
		t.Errorf("the capture hook was clobbered by keep-going:\n%s", out)
	}
	if !strings.Contains(out, "keepgoing-hook") {
		t.Errorf("the keep-going hook was lost:\n%s", out)
	}
}
