package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Deleting a claude session moves its one store file to the trash and leaves a manifest saying
// where it came from. The file must not merely be unlinked: "delete" in the launcher has to be
// recoverable, because it acts on whichever row the cursor happens to be on.
func TestDeleteMovesTheStoreFileToTheTrash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store := filepath.Join(home, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(store, "abc123.jsonl")
	if err := os.WriteFile(f, []byte(`{"role":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := aiSession{Tool: "claude", ID: "abc123", Cwd: "/tmp/proj", Title: "a session", storePath: f}
	meta := map[string]sessMeta{"abc123": {Pinned: true}}

	dir, err := deleteSession(s, meta)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Error("the store file is still in place; the session would come back on the next scan")
	}
	if _, err := os.Stat(filepath.Join(dir, "abc123.jsonl")); err != nil {
		t.Errorf("the conversation was destroyed rather than moved: %v", err)
	}
	// The sidecar entry must go too, or a pin outlives the session it described.
	if _, still := meta["abc123"]; still {
		t.Error("curation state outlived the session")
	}

	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("no manifest — restoring by hand would be guesswork: %v", err)
	}
	var man trashManifest
	if err := json.Unmarshal(b, &man); err != nil {
		t.Fatal(err)
	}
	if man.Tool != "claude" || man.ID != "abc123" {
		t.Errorf("manifest does not identify the session: %+v", man)
	}
	if len(man.Files) != 1 || man.Files[0].From != f {
		t.Errorf("manifest does not record where the file came from: %+v", man.Files)
	}
}

// A live session is refused. The engine rewrites its store on every turn, so moving the file
// mid-turn either gets it recreated underneath us or loses the turn in flight.
func TestDeleteRefusesALiveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	f := filepath.Join(home, "live.jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := aiSession{Tool: "claude", ID: "live1", Live: true, storePath: f}
	if _, err := deleteSession(s, nil); err == nil {
		t.Fatal("deleted a running session")
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("the refusal still moved the file")
	}
}

// `llm` keeps every conversation in one shared SQLite log, so there is no single file to move.
// Deleting one would mean writing into another tool's database, which is the line llms_prune.go
// draws. It must be refused rather than half-attempted.
func TestDeleteRefusesToolsWithoutAFilePerSession(t *testing.T) {
	if _, ok := sessionFiles(aiSession{Tool: "llm", ID: "c1"}); ok {
		t.Error("llm sessions reported as individually deletable")
	}
	if _, err := deleteSession(aiSession{Tool: "llm", ID: "c1"}, nil); err == nil {
		t.Error("deleting an llm conversation was allowed")
	}
}

// An antigravity session is the .db AND the brain/<id>/ tree. storePath points at a transcript
// inside that tree, so moving only storePath would leave a resumable session with no title —
// corruption, not deletion.
func TestAntigravityDeleteTakesTheDatabaseAndTheBrainTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".gemini", "antigravity-cli")
	transcript := filepath.Join(base, "brain", "conv9", ".system_generated", "logs", "transcript.jsonl")

	paths, ok := sessionFiles(aiSession{Tool: "antigravity", ID: "conv9", storePath: transcript})
	if !ok {
		t.Fatal("antigravity reported as not deletable")
	}
	want := map[string]bool{
		filepath.Join(base, "conversations", "conv9.db"): false,
		filepath.Join(base, "brain", "conv9"):            false,
	}
	for _, p := range paths {
		if _, expected := want[p]; !expected {
			t.Errorf("unexpected path %q", p)
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("missing %q — a partial delete leaves a broken session behind", p)
		}
	}
}

// Deleting a session whose files are already gone reports that plainly and leaves no empty trash
// entry claiming a deletion happened.
func TestDeleteOfAlreadyMissingFilesLeavesNoEmptyTrashEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := aiSession{Tool: "claude", ID: "ghost", storePath: filepath.Join(home, "nope.jsonl")}
	if _, err := deleteSession(s, nil); err == nil {
		t.Fatal("reported success for a session with nothing on disk")
	}
	entries, _ := os.ReadDir(sessionTrashDir())
	if len(entries) != 0 {
		t.Errorf("left %d trash entr(ies) behind for a no-op delete", len(entries))
	}
}

// moveInto must survive src and dst being on different filesystems, because an adopted root can
// be any directory (llms_roots.go). The rename path is covered by the tests above; this pins the
// copy fallback by moving a directory, which os.Rename handles but copyTree must too.
func TestMoveIntoHandlesADirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "x.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "moved")
	if err := moveInto(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sub", "x.txt")); err != nil {
		t.Errorf("nested file did not survive the move: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source survived the move")
	}
}

// The key table drives both the `?` overlay and `ptln help`, so a key the manager handles but the
// table omits is invisible to every user who looks it up.
func TestDeleteKeyIsDocumented(t *testing.T) {
	for _, row := range aiMenuKeys {
		if row[0] == "x" {
			if row[1] == "" {
				t.Error("x is listed with no description")
			}
			return
		}
	}
	t.Error("x (delete) is missing from aiMenuKeys, so neither ? nor `ptln help` mentions it")
}

// x on a RUNNING session must refuse outright and never open the confirmation. There is no answer
// to "are you sure" that makes moving a store the engine is rewriting safe, so offering the choice
// would only be a way to corrupt it.
func TestXRefusesARunningSessionWithoutOpeningTheModal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := filepath.Join(home, "live.jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &aiMenu{view: []aiSession{{Tool: "claude", ID: "live", Title: "working", Cwd: "/p", storePath: f, Live: true}}}
	m.collapsed = map[string]bool{"/p": false}
	m.buildRows()
	m.selectByID("live")

	m.handleKey([]byte{'x'})
	if m.confirmDelete != nil {
		t.Error("the confirmation opened for a running session")
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("a running session's store was moved")
	}
	if !strings.Contains(m.flash, "running") {
		t.Errorf("the refusal does not say why: %q", m.flash)
	}
}

// x opens the confirmation and deletes nothing by itself; only [y] acts.
func TestXOpensTheConfirmationAndOnlyYDeletes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mk := func(id string) aiSession {
		f := filepath.Join(home, id+".jsonl")
		if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return aiSession{Tool: "claude", ID: id, Title: id, Cwd: "/p", storePath: f, LastActive: time.Now()}
	}
	one := mk("one")

	m := &aiMenu{view: []aiSession{one}}
	m.collapsed = map[string]bool{"/p": false}
	m.buildRows()
	m.selectByID("one")

	m.handleKey([]byte{'x'})
	if m.confirmDelete == nil || m.confirmDelete.ID != "one" {
		t.Fatal("X did not open the confirmation for the highlighted session")
	}
	if _, err := os.Stat(one.storePath); err != nil {
		t.Fatal("X deleted before being confirmed")
	}

	// n cancels, and the file stays.
	m.handleKey([]byte{'n'})
	if m.confirmDelete != nil {
		t.Error("the modal stayed up after being answered")
	}
	if _, err := os.Stat(one.storePath); err != nil {
		t.Error("cancel still deleted the session")
	}

	// y acts.
	m.handleKey([]byte{'x'})
	m.handleKey([]byte{'y'})
	if _, err := os.Stat(one.storePath); !os.IsNotExist(err) {
		t.Error("[y] did not delete the session")
	}
}

// The modal must own input: while it is up, a key that normally does something else must not do
// that thing. Pressing j (move down) is a cancel, not a cursor move with a modal left hanging.
func TestTheConfirmationOwnsInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := filepath.Join(home, "s.jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &aiMenu{view: []aiSession{{Tool: "claude", ID: "s", Title: "s", Cwd: "/p", storePath: f}}}
	m.collapsed = map[string]bool{"/p": false}
	m.buildRows()
	m.selectByID("s")
	m.handleKey([]byte{'x'})

	before := m.cursor
	m.handleKey([]byte{'j'})
	if m.confirmDelete != nil {
		t.Error("the modal survived an unrelated key")
	}
	if m.cursor != before {
		t.Error("j moved the cursor while the modal was up instead of cancelling")
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("an unrelated key deleted the session")
	}
}

// The confirmation has to carry the facts that would stop you: uncommitted work, an active
// worktree, recent use, and other running sessions in the same project. A title and an id alone
// cannot tell you whether this is the row you meant.
func TestConfirmationShowsTheReasonsNotToDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := aiSession{
		Tool: "claude", ID: "abc12345", Title: "refactor the parser",
		Cwd: "/p/wt", ProjDir: "/p", WtName: "feat-parser",
		LastActive: time.Now().Add(-10 * time.Minute),
		storePath:  filepath.Join(home, "abc12345.jsonl"),
	}
	peers := []aiSession{s, {ID: "other", Live: true, ProjDir: "/p"}}

	f := gatherDeleteFacts(s, peers)
	if f.Worktree != "feat-parser" {
		t.Errorf("an active worktree was not reported: %q", f.Worktree)
	}
	if !f.Recent {
		t.Error("a session used 10 minutes ago was not flagged as recent")
	}
	if f.LiveSibs != 1 {
		t.Errorf("other running sessions in the project not counted: %d", f.LiveSibs)
	}

	// A dead worktree is NOT an active tree — that is the case prune exists for.
	dead := s
	dead.DeadWt = true
	if gatherDeleteFacts(dead, nil).Worktree != "" {
		t.Error("a deleted worktree was reported as active")
	}

	body := strings.Join(deleteModalLines(s, f), "\n")
	for _, want := range []string{"refactor the parser", "feat-parser", "last used", "running", "[y] delete", "[n] cancel"} {
		if !strings.Contains(body, want) {
			t.Errorf("the confirmation does not mention %q:\n%s", want, body)
		}
	}
	// Uncommitted work is the strongest reason to stop, so it must appear when present. Gone is
	// cleared because a directory that no longer exists cannot hold uncommitted changes — the
	// modal reports the missing directory instead, and that precedence is deliberate.
	withDirty := f
	withDirty.Gone = false
	withDirty.Branch, withDirty.Dirty = "feat/parser", 3
	if b := strings.Join(deleteModalLines(s, withDirty), "\n"); !strings.Contains(b, "3 uncommitted") {
		t.Errorf("uncommitted count missing from the confirmation:\n%s", b)
	}
}

// The age in the confirmation must read the way the rest of the manager reads. shortDuration caps
// at hours, so a month-old session rendered as "644h54m ago" beside a detail pane saying "26d ago".
func TestConfirmationAgeMatchesTheRestOfTheUI(t *testing.T) {
	old := aiSession{Tool: "claude", ID: "x", LastActive: time.Now().Add(-26 * 24 * time.Hour)}
	f := gatherDeleteFacts(old, nil)
	if f.Age != "26d" {
		t.Errorf("want 26d (what the detail pane shows), got %q", f.Age)
	}
	if f.Recent {
		t.Error("a 26-day-old session is not recent")
	}
	fresh := gatherDeleteFacts(aiSession{Tool: "claude", ID: "y", LastActive: time.Now().Add(-90 * time.Minute)}, nil)
	if fresh.Age != "1h" || !fresh.Recent {
		t.Errorf("90 minutes ago: age=%q recent=%v", fresh.Age, fresh.Recent)
	}
}

// Archive is gone. It existed so a session could be hidden without touching the engine's file, but
// hiding a row and deleting it were indistinguishable at the moment the key was pressed — the row
// vanished either way — and because nothing ever left, the list only grew. A reintroduced
// `archived` flag would bring that confusion straight back.
func TestThereIsNoArchiveConcept(t *testing.T) {
	// The sidecar must not carry the flag again: an archived session is one the launcher hides
	// while its file sits on disk, which is the thing that read as a silent delete.
	b, err := json.Marshal(sessMeta{Pinned: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "archiv") {
		t.Errorf("sessMeta serialises an archive flag again: %s", b)
	}
	// And the key table must not advertise one.
	for _, row := range aiMenuKeys {
		if strings.Contains(strings.ToLower(row[1]), "archive") {
			t.Errorf("the key table still offers archive: %q → %q", row[0], row[1])
		}
	}
}

// An old sidecar written before archive was removed must still load. The flag is simply ignored,
// which means those sessions reappear in the list — correct, since hiding no longer exists — and
// the fields that still matter must survive alongside it.
func TestAnOldSidecarWithArchivedStillLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"abc":{"archived":true,"pinned":true,"name":"kept"}}`
	if err := os.WriteFile(llmMetaPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m := loadLLMMeta()
	got, ok := m["abc"]
	if !ok {
		t.Fatal("an old sidecar entry was dropped entirely")
	}
	if !got.Pinned || got.Name != "kept" {
		t.Errorf("fields beside the dead flag were lost: %+v", got)
	}
}
