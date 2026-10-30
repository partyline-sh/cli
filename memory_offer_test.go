package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// The first session in a repo with no memory is offered setup; nobody has to know the commands.
// It is offered ONCE per repo — every worktree of it included — so declining is not met with the
// same question every turn.
func TestMemoryOfferIsMadeOncePerRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	if got := memoryOffer(repo); !strings.Contains(got, "ptln memory build") {
		t.Fatalf("first turn in a repo with no memory should carry the setup offer, got %q", got)
	}
	if got := memoryOffer(repo); got != "" {
		t.Errorf("the offer must not repeat in the same repo, got %q", got)
	}
	wt := t.TempDir() + "/wt"
	gitIn(t, repo, "worktree", "add", "-q", wt)
	if got := memoryOffer(wt); got != "" {
		t.Errorf("a worktree is the same repo and must not be offered again, got %q", got)
	}
}

func TestMemoryOfferIsSilentWhereThereIsNothingToOffer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := memoryOffer(t.TempDir()); got != "" {
		t.Errorf("outside a git repo there is nowhere to put memory, got %q", got)
	}
	repo := initRepo(t)
	if _, err := writeFact(repo, fact{Kind: "gotcha", By: "T", At: time.Now(),
		Body: "The importer rejects CSV files with a byte-order mark."}); err != nil {
		t.Fatal(err)
	}
	if got := memoryOffer(repo); got != "" {
		t.Errorf("a repo that already has memory needs no setup offer, got %q", got)
	}
}

// Capture must not create memory/ in a repo nobody set up — that would litter every repo a person
// ever opened a session in, including the ones where they declined the offer.
func TestMemoryTargetNeedsMemoryToExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	if _, _, _, ok := memoryTarget(repo); ok {
		t.Fatal("a repo with no memory/ must not be a capture target")
	}
	if _, err := writeFact(repo, fact{Kind: "gotcha", By: "T", At: time.Now(),
		Body: "The importer rejects CSV files with a byte-order mark."}); err != nil {
		t.Fatal(err)
	}
	if dir, _, _, ok := memoryTarget(repo); !ok || dir == "" {
		t.Fatal("once memory/ exists the repo is the target")
	}
}

// The argv a context-threads-era partyline saved, exactly as `--resume` found it in
// llms-workspace.json. Resuming it used to relaunch claude with a dead MCP server and a primer
// naming tools that no longer exist, because the spec carried a Thread and preparation skipped it.
func TestPrepareLaunchSpecDropsContextThreadWiring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	saved := Spec{
		Label: "XERO RECEIPTS", Key: "f37f2dc8", Dir: repo,
		Thread: "fa2cb771-1e9d-436c-9fa4-34bf7596ad11", ThreadLabel: "xero",
		Argv: []string{"claude", "--resume", "f37f2dc8", "--permission-mode", "bypassPermissions",
			"--mcp-config", `{"mcpServers":{"partyline-context-threads":{"args":["cg-mcp"],"command":"/opt/homebrew/bin/ptln"}}}`,
			"--append-system-prompt", "## Shared team context (partyline Context Threads)\n\n..."},
	}
	got := prepareLaunchSpec(saved)

	joined := strings.Join(got.Argv, " ")
	for _, dead := range []string{"--mcp-config", "--append-system-prompt", "cg-mcp", "Context Threads"} {
		if strings.Contains(joined, dead) {
			t.Errorf("resumed argv still carries %q: %v", dead, got.Argv)
		}
	}
	if got.Thread != "" || got.ThreadLabel != "" {
		t.Errorf("thread should be cleared, got %q / %q", got.Thread, got.ThreadLabel)
	}
	if want := []string{"claude", "--resume", "f37f2dc8", "--permission-mode", "bypassPermissions"}; !reflect.DeepEqual(got.Argv[:5], want) {
		t.Errorf("the conversation and permission mode must survive: got %v", got.Argv)
	}
	if !strings.Contains(joined, "memory-hook") || !strings.Contains(joined, "memory-capture-hook") {
		t.Errorf("a resumed claude session in a repo should get the memory hooks: %v", got.Argv)
	}
	if again := prepareLaunchSpec(got); !reflect.DeepEqual(again.Argv, got.Argv) {
		t.Errorf("preparation must be idempotent:\n first %v\nsecond %v", got.Argv, again.Argv)
	}
}

func TestWithMemoryHooksLeavesOtherLaunchesAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	if got := withMemoryHooks("codex", repo, []string{"codex"}); len(got) != 1 {
		t.Errorf("codex takes its hook from its own config, not a flag: %v", got)
	}
	if got := withMemoryHooks("claude", t.TempDir(), []string{"claude"}); len(got) != 1 {
		t.Errorf("outside a git repo there is no memory to hook: %v", got)
	}
	kg := []string{"claude", "--settings", `{"hooks":{"Stop":[]}}`}
	if got := withMemoryHooks("claude", repo, kg); !reflect.DeepEqual(got, kg) {
		t.Errorf("an argv that already has --settings (keep-going) must not get a second: %v", got)
	}

	stale := []string{"claude", "--resume", "x", "--settings", strings.ReplaceAll(sessionSettings(nil), selfExe(), "/old/place/ptln")}
	got := withMemoryHooks("claude", repo, stale)
	if len(got) != len(stale) || got[4] != sessionSettings(nil) {
		t.Errorf("memory hooks saved with an old binary path must be regenerated, not kept: %v", got)
	}
}

// Keep-going's real settings carry the memory hooks too; preparing a resumed keep-going session
// must leave them exactly as they are, or the session stops auto-continuing.
func TestWithMemoryHooksKeepsRealKeepGoingSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	kg := []string{"claude", "--resume", "x", "--settings", keepGoingSettings("kg-1")}
	if got := withMemoryHooks("claude", repo, kg); !reflect.DeepEqual(got, kg) {
		t.Errorf("keep-going settings were rewritten: %v", got)
	}
}

// One rule decides where facts live, for the hooks and for `ptln memory add` alike. A repo bound
// to a legacy project with no memory/ of its own keeps writing to the project; otherwise the first
// fact an agent recorded would create memory/ in the repo and orphan the project's facts.
func TestMemoryAddAndHooksAgreeOnTheStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	pdir := projectWorkspaceDir("acr")
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveProjectFile(pdir, projectFile{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"project":"acr"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	want, _, _, ok := memoryTarget(repo)
	if !ok {
		t.Fatal("a repo bound to a project should resolve to it")
	}
	ws, _ := workspaceHere()
	if ws.Dir != want {
		t.Errorf("ptln memory add would write to %q but the hooks read %q", ws.Dir, want)
	}
}
