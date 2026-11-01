package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests come from the first real `ptln memory build` run, on acr-odoo-mcp.

// The overview may mention a PR; it is a description, not a one-line claim. A correct overview
// was refused on edit because rewrite and append skipped the exemption that add had.
func TestOverviewEditPassesTheGate(t *testing.T) {
	dir := tmpWorkspace(t)
	body := "acr-odoo-mcp exposes Odoo helpdesk and project data to agents over MCP. It replaced the scripts in PR #12, which was merged in March."
	if _, err := writeFact(dir, fact{Kind: "overview", By: "T", At: time.Now(), Body: body}); err != nil {
		t.Fatalf("add refused the overview: %v", err)
	}
	facts, err := readFacts(dir, false)
	if err != nil || len(facts) != 1 {
		t.Fatalf("readFacts = %d, %v", len(facts), err)
	}
	if _, err := rewriteFact(dir, facts[0].ID, body+" It runs on Cloud Run.", "T"); err != nil {
		t.Errorf("edit refused the overview: %v", err)
	}
	if _, err := appendToFact(dir, facts[0].ID, "PR #40 was merged to add ticket search.", "T"); err != nil {
		t.Errorf("append refused the overview: %v", err)
	}
}

// A refusal names the words that tripped it, so the caller can reword instead of guessing.
func TestRefusalQuotesTheMatchedWords(t *testing.T) {
	err := screenFact("constraint", "PR #12 was merged without the migration, so the schema lags behind.").gateError()
	if err == nil {
		t.Fatal("work-item status was not refused")
	}
	if !strings.Contains(err.Error(), `matched: "PR #12 was`) {
		t.Errorf("refusal does not quote the match:\n%s", err)
	}
}

// `ptln memory add trap ...` should say which kind was meant.
func TestUnknownKindSuggestsTheRealOne(t *testing.T) {
	for in, want := range map[string]string{"trap": "gotcha", "traps": "gotcha", "rule": "constraint", "decis": "decision"} {
		if msg := unknownKindError(in).Error(); !strings.Contains(msg, want) || !strings.Contains(msg, "use one of") {
			t.Errorf("%q: %s", in, msg)
		}
	}
	if _, err := writeFact(tmpWorkspace(t), fact{Kind: "trap", At: time.Now(), Body: "Odoo's XML-RPC returns False, not null, for empty many2one fields."}); err == nil || !strings.Contains(err.Error(), "gotcha") {
		t.Errorf("writeFact with kind trap: %v", err)
	}
}

// `ptln memory add --help` printed an error about the kind "--help".
func TestMemorySubcommandHelpPrintsHelp(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	memoryMain([]string{"add", "--help"})
	os.Stdout = stdout
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "USAGE") {
		t.Fatalf("no help printed:\n%s", out)
	}
	for _, k := range factKinds {
		if !strings.Contains(string(out), k) {
			t.Errorf("memory add --help does not list kind %q:\n%s", k, out)
		}
	}
}

// The overview is written from the last commit. Uncommitted, rejected code was described as a
// component of acr-odoo-mcp.
func TestExportHeadLeavesOutUncommittedFiles(t *testing.T) {
	repo := initRepo(t)
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	if _, _, err := exportHead(repo); err == nil {
		t.Error("exportHead succeeded on a repo with no commits")
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// export-ignore is for release tarballs; the overview still needs to see CI config.
	if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".github/ci.yml": "on: push\n", ".gitattributes": ".github export-ignore\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, "rejected.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := exportHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Base(dir) != filepath.Base(repo) {
		t.Errorf("exported to %s; the directory should carry the repo's name", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "ci.yml")); err != nil {
		t.Error("an export-ignore path was left out")
	}
	if out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); !strings.Contains(string(out), " M main.go") || !strings.Contains(string(out), "?? rejected.go") {
		t.Errorf("export changed the repo's index or working tree:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "rejected.go")); err == nil {
		t.Error("untracked file was exported")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.go")); strings.Contains(string(b), "edited") {
		t.Error("uncommitted edit was exported")
	}
}

// The seeded questions ask what only the people who own the repo know, not what the code shows.
func TestBuildQuestionsAskAboutPurpose(t *testing.T) {
	topics := map[string]bool{}
	for _, q := range buildQuestions {
		topics[q.Topic] = true
	}
	for _, want := range []string{"q-purpose", "q-owner", "q-never-change", "q-deploy"} {
		if !topics[want] {
			t.Errorf("no %s question; have %v", want, topics)
		}
	}
}

// A -h inside an unquoted fact body is part of the fact.
func TestHelpFlagInsideABodyIsNotHelp(t *testing.T) {
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	stdout := os.Stdout
	devnull, _ := os.Open(os.DevNull)
	os.Stdout = devnull
	memoryMain(strings.Fields("add gotcha grep -h drops the filename prefix from multi-file output"))
	os.Stdout = stdout
	facts, err := readFacts(repo, false)
	if err != nil || len(facts) != 1 || !strings.Contains(facts[0].Body, "grep -h") {
		t.Errorf("fact not recorded: %v %+v", err, facts)
	}
}
