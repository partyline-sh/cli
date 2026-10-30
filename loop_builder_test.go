package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// stepBrief is a step whose acceptance is that file exists.
func stepBrief(title, file string) string {
	return strings.Replace(strings.Replace(goodBrief, "# CSV export", "# "+title, 1), "- run: test -f export.csv\n- file: export.go", "- run: test -f "+file, 1)
}

// builderStub is an engine for TestLoopBuilder: each call is one builder run, which sees its prompt,
// may edit the checkout and returns its final message.
type builderStub struct {
	mu      sync.Mutex
	prompts []string
	steps   []func(dir, prompt string) string
}

func (s *builderStub) run(_ context.Context, _ loopEngine, dir, prompt, _ string, out io.Writer) (string, loopTokens, error) {
	s.mu.Lock()
	n := len(s.prompts)
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	if n >= len(s.steps) {
		return "", loopTokens{}, fmt.Errorf("builder %d was not expected", n+1)
	}
	fmt.Fprintf(out, "stub builder %d\n", n+1)
	return s.steps[n](dir, prompt), loopTokens{}, nil
}

func stubReport(extra string) string {
	return "## Evidence\nthe stub ran\n## Hardest decision\nnone\n## Rejected alternatives\nnone\n## Least confident\nnone\n## What an excellent reviewer would criticise\nnothing\n" + extra
}

func p1(file, quote string) string {
	return fmt.Sprintf(`{"findings":[{"priority":"P1","file":%q,"line":1,"quote":%q,"claim":"%s is wrong","fix":"fix it"}],"verdict":"changes_required"}`, file, quote, file)
}

const approve = `{"findings":[],"verdict":"approve"}`

// A two-step batch runs to the end with no session action: builders build each step, partyline
// submits each round with the builder's report, commits each passed step exactly as reviewed
// (authored by the builder, committed by the person, with trailers) and starts the next; a failing
// acceptance check and a P1 each lead to another builder with them in its prompt; a dispute in a
// builder's report reaches the person in the deferred review.
func TestLoopBuilder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Error(err)
		}
	}
	stub := &builderStub{steps: []func(dir, prompt string) string{
		// Step 1: the first build misses the acceptance file, the second adds it, the third fixes the P1.
		func(dir, _ string) string {
			write(dir, "a.txt", "alpha holds the first value\n")
			write(dir, "memory/fact.md", "a fact the person commits later\n") // must not hold the batch up
			return stubReport("")
		},
		func(dir, _ string) string { write(dir, "ok1.txt", "one\n"); return stubReport("") },
		func(dir, _ string) string {
			write(dir, "a.txt", "alpha holds the fixed value\n")
			return stubReport("")
		},
		// Step 2: the first build draws a P1, the second disputes it and changes nothing.
		func(dir, _ string) string {
			write(dir, "ok2.txt", "two holds the second value\n")
			return stubReport("")
		},
		func(string, string) string { return stubReport("## Disputes\nF1-1: two is the intended text\n") },
	}}
	reviews := []string{p1("a.txt", "alpha holds the first value"), approve, p1("ok2.txt", "two holds the second value"), p1("ok2.txt", "two holds the second value")}
	var reviewMu sync.Mutex
	oldBuilder, oldReviewer, oldStart, oldStartC, oldAdvance, oldSpawn := runBuilder, runReviewer, startLoopBuilder, startContractor, builderAdvance, spawnRound
	t.Cleanup(func() {
		runBuilder, runReviewer, startLoopBuilder, startContractor, builderAdvance, spawnRound = oldBuilder, oldReviewer, oldStart, oldStartC, oldAdvance, oldSpawn
	})
	runBuilder, startLoopBuilder, builderAdvance = stub.run, realStartLoopBuilder, realBuilderAdvance
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		reviewMu.Lock()
		defer reviewMu.Unlock()
		if len(reviews) == 0 {
			return "", loopTokens{}, fmt.Errorf("no review expected")
		}
		out := reviews[0]
		reviews = reviews[1:]
		return out, loopTokens{}, nil
	}
	// Contractors and rounds run in this process, each on its own goroutine as they would in their
	// own window or process; each waits for the lock its starter holds.
	var wg sync.WaitGroup
	startContractor = func(c *contractor) error {
		c.State, c.Started = "running", time.Now().UTC()
		if err := withContractors(c.save); err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := runLoopBuild(c.Argv[3:])
			finishContractor(c.ID, err, "")
		}()
		return nil
	}
	spawnRound = func(id string, n int) error {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := loadLoopRun(id)
			for ; err == nil && r.State != loopBaseline && r.State != loopChecking; r, err = loadLoopRun(id) {
				time.Sleep(10 * time.Millisecond) // the starter's lock has not saved the state yet
			}
			if n == 0 {
				loopBaselineRound(r)
			} else {
				loopRunRound(r, n)
			}
		}()
		return nil
	}

	briefs := []string{stepBrief("One", "ok1.txt"), stepBrief("Two", "ok2.txt")}
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", briefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	base, _ := gitMem(repo, "rev-parse", "HEAD")
	r, _ = loadLoopRun(r.ID)
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	var deferral *loopRun
	for deadline := time.Now().Add(20 * time.Second); deferral == nil; time.Sleep(50 * time.Millisecond) {
		if b, _ = loadBatch(b.ID); b.Deferral != "" {
			if d, _ := loadLoopRun(b.Deferral); d != nil && d.State == loopAwaitYou {
				deferral = d
			}
		}
		if time.Now().After(deadline) {
			var trail strings.Builder
			for _, id := range b.Runs {
				evs, _ := loopEvents(id)
				for _, e := range evs {
					fmt.Fprintf(&trail, "%s %s %s %s %v\n", id, e.Type, e.State, e.Note, e.Data)
				}
				fmt.Fprintf(&trail, "%s inbox: %q\n", id, loopTake(id))
			}
			t.Fatalf("the batch did not reach the deferred review after %d builder runs:\n%s", len(stub.prompts), trail.String())
		}
	}
	wg.Wait()

	if len(stub.prompts) != 5 {
		t.Fatalf("%d builder runs, want 5", len(stub.prompts))
	}
	if p := stub.prompts[1]; !strings.Contains(p, "test -f ok1.txt") || !strings.Contains(p, "did not fail before") {
		t.Errorf("the failing check is not in the next builder's prompt:\n%s", p)
	}
	if p := stub.prompts[2]; !strings.Contains(p, "F2-1") || !strings.Contains(p, "a.txt is wrong") {
		t.Errorf("the finding is not in the next builder's prompt:\n%s", p)
	}
	if !strings.Contains(stub.prompts[0], "# One") || !strings.Contains(stub.prompts[3], "# Two") || !strings.Contains(stub.prompts[0], builderClosing) {
		t.Errorf("a builder did not get its step's brief and the closing instruction")
	}

	// Two commits on the batch's branch, each the reviewed tree, by the builder, committed by the person.
	log, _ := gitMem(repo, "log", "--format=%H|%an <%ae>|%cn <%ce>|%s", base+"..HEAD")
	lines := strings.Split(log, "\n")
	if len(lines) != 2 {
		t.Fatalf("commits after the base:\n%s", log)
	}
	for i, line := range lines {
		f := strings.Split(line, "|")
		step := 2 - i
		if f[1] != "claude via partyline <partyline@localhost>" || f[2] != "T <t@example.com>" || f[3] != []string{"One", "Two"}[step-1] {
			t.Errorf("step %d commit: %s", step, line)
		}
		body, _ := gitMem(repo, "log", "-1", "--format=%B", f[0])
		run, _ := loadLoopRun(b.Runs[step-1])
		for _, want := range []string{"Loop: " + run.ID, fmt.Sprintf("Batch: %s step %d", b.ID, step), fmt.Sprintf("Round: %d", run.Round)} {
			if !strings.Contains(body, want) {
				t.Errorf("step %d commit lacks %q:\n%s", step, want, body)
			}
		}
		if got := treeWithoutMemory(t, repo, f[0]); got != run.LastTree {
			t.Errorf("step %d commit's tree %s is not the reviewed tree %s", step, got, run.LastTree)
		}
		if err := exec.Command("git", "-C", repo, "cat-file", "-e", f[0]+":memory/committed.md").Run(); err != nil {
			t.Errorf("step %d commit dropped memory/ that the review leaves out", step)
		}
	}
	if status, _ := gitMem(repo, "status", "--porcelain"); status != "?? memory/fact.md" {
		t.Errorf("after the commits the checkout should hold only the uncommitted fact:\n%s", status)
	}
	if b.Deferred[0].ID != "S2-F1-1" || b.Deferred[0].Dispute != "two is the intended text" {
		t.Errorf("the dispute did not reach the person: %+v", b.Deferred)
	}
	// The session is told the batch is waiting for the person and how it ended; it is never asked to
	// build, submit a round, commit or start a step.
	for _, id := range b.Runs[:2] {
		for _, m := range loopTake(id) {
			for _, ask := range []string{"loop ready", "batch next", "Fix this", "Commit"} {
				if strings.Contains(m, ask) {
					t.Errorf("step %s asked the session to %q: %s", id, ask, m)
				}
			}
		}
	}
}

// treeWithoutMemory is commit's tree with memory/ and .ptln taken out, as the review sees it.
func treeWithoutMemory(t *testing.T, repo, commit string) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index")
	if _, err := gitIndex(repo, index, "read-tree", commit); err != nil {
		t.Fatal(err)
	}
	if _, err := gitIndex(repo, index, "rm", "-r", "-q", "--cached", "--ignore-unmatch", "--", "memory", ".ptln"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitIndex(repo, index, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// The builder's argv and environment. claude: headlessClaude's restrictions, the builder's tools,
// edits approved by acceptEdits and Bash by --allowedTools; codex: workspace-write. Neither gets a
// token, the tmux server or the session's identity. The claude stream from a real run (claude
// 2.1.312, testdata/builder-claude-stream.jsonl) reads as its tool calls and final message.
func TestLoopBuilderInvocation(t *testing.T) {
	t.Setenv("GH_TOKEN", "gho_x")
	t.Setenv("TMUX", "/tmp/tmux-1/partyline,1,0")
	t.Setenv("PARTYLINE_SESSION_KEY", "pane:%1")
	dir := t.TempDir()
	cmd := builderClaudeCmd(context.Background(), loopEngine{Name: "claude", Model: "haiku"}, dir)
	args := cmd.Args[1:]
	// The person's own claude: no --restricted (it hid CLAUDE.md), no tool or MCP limits.
	for _, pair := range [][2]string{{"--permission-mode", "bypassPermissions"}, {"--output-format", "stream-json"}, {"--model", "haiku"}} {
		if j := slices.Index(args, pair[0]); j < 0 || j+1 >= len(args) || args[j+1] != pair[1] {
			t.Errorf("claude builder lacks %s %s: %q", pair[0], pair[1], args)
		}
	}
	for _, flag := range []string{"--restricted", "--strict-mcp-config", "--tools", "--allowedTools"} {
		if slices.Contains(args, flag) {
			t.Errorf("claude builder still limited by %s: %q", flag, args)
		}
	}
	for _, flag := range []string{"-p", "--verbose", "--no-session-persistence"} {
		if !slices.Contains(args, flag) {
			t.Errorf("claude builder lacks %s: %q", flag, args)
		}
	}
	if cmd.Dir != dir {
		t.Errorf("claude builder runs in %q", cmd.Dir)
	}
	// partyline's state is out of reach of the file tools and of Bash; nothing else is denied.
	j := slices.Index(args, "--settings")
	var settings struct {
		Permissions struct{ Deny []string }
		Sandbox     struct {
			Enabled, FailIfUnavailable bool
			AllowUnsandboxedCommands   *bool
			Filesystem                 struct{ DenyRead, AllowWrite []string }
		}
	}
	if j < 0 || json.Unmarshal([]byte(args[j+1]), &settings) != nil {
		t.Fatalf("claude builder has no settings: %q", args)
	}
	for _, rule := range []string{"Read(~/.partyline/**)", "Edit(~/.partyline/**)", "Write(~/.partyline/**)"} {
		if !slices.Contains(settings.Permissions.Deny, rule) {
			t.Errorf("claude builder does not deny %s: %v", rule, settings.Permissions.Deny)
		}
	}
	sb := settings.Sandbox
	if !sb.Enabled || !sb.FailIfUnavailable || sb.AllowUnsandboxedCommands == nil || *sb.AllowUnsandboxedCommands ||
		!slices.Equal(sb.Filesystem.DenyRead, []string{"~/.partyline"}) {
		t.Errorf("sandbox: %+v", sb)
	}
	want := []string{"exec", "-", "--json", "-s", "workspace-write", "-c", "sandbox_workspace_write.network_access=true", "-C", dir, "-o", "/l", "--skip-git-repo-check", "--ephemeral", "-m", "gpt"}
	if got := codexBuildArgs(loopEngine{Name: "codex", Model: "gpt"}, dir, "/l"); !slices.Equal(got, want) {
		t.Errorf("codex builder:\n got %q\nwant %q", got, want)
	}
	// The environment: the session identity gone, gh logged out, every push URL unreachable.
	env := strings.Join(cmd.Env, "\n")
	for _, bad := range []string{"GH_TOKEN=", "TMUX=", "PARTYLINE_SESSION_KEY="} {
		if strings.Contains("\n"+env, "\n"+bad) {
			t.Errorf("the builder's environment has %s", bad)
		}
	}
	for _, need := range []string{"GH_CONFIG_DIR=", "GIT_CONFIG_COUNT=5", "pushInsteadOf", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(env, need) {
			t.Errorf("the builder's environment lacks %s", need)
		}
	}

	f, err := os.Open("testdata/builder-claude-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var shown strings.Builder
	result, failure, tok := renderBuilderStream("claude", f, &shown)
	if result != "DONE" || failure != "" {
		t.Errorf("result %q, failure %q", result, failure)
	}
	// The fixture's result: 6 input + 3101 cache writes, 258 output, 21095 cache reads.
	if tok.In != 3107 || tok.Out != 258 || tok.Cached != 21095 || tok.CostUSD < 0.00096 || tok.CostUSD > 0.00097 {
		t.Errorf("claude tokens %+v", tok)
	}
	for _, want := range []string{"→ Write /work/hello.txt", "→ Bash cat hello.txt", "DONE"} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("the live view lacks %q:\n%s", want, shown.String())
		}
	}
	codex := `{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}
{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go"}]}}
{"type":"item.completed","item":{"type":"agent_message","text":"done\u001b[2J"}}
{"type":"turn.completed","usage":{"input_tokens":17018,"cached_input_tokens":12288,"cache_write_input_tokens":0,"output_tokens":51,"reasoning_output_tokens":44}}
{"type":"turn.failed","error":{"message":"usage limit"}}`
	shown.Reset()
	_, failure, tok = renderBuilderStream("codex", strings.NewReader(codex), &shown)
	// codex-cli 0.164.0: cached input is part of input, reasoning part of output.
	if tok != (loopTokens{In: 17018 - 12288, Out: 51, Cached: 12288}) {
		t.Errorf("codex tokens %+v", tok)
	}
	if s := shown.String(); !strings.Contains(s, "→ go test ./...") || !strings.Contains(s, "→ edit a.go") || !strings.Contains(s, "done") || strings.Contains(s, "\x1b") {
		t.Errorf("codex live view:\n%q", s)
	}
	if !strings.Contains(failure, "usage limit") {
		t.Errorf("codex failure %q", failure)
	}
}

// A build that fails, or ends without a report, starts another builder: one asked only for the
// report, then a failed one; three failures in a row fail the step. A lost builder is started again.
func TestLoopBuilderRetries(t *testing.T) {
	_, _, r := approvedBatch(t)
	loopBaselineRound(r)
	r, _ = loadLoopRun(r.ID)
	if r.State != loopBuilding || r.Builds != 1 || !strings.Contains(startedBuilders[len(startedBuilders)-1], "Build the step") {
		t.Fatalf("after the baseline: %s, %d builds", r.State, r.Builds)
	}
	loopBuilderDone(r.ID, 1, "all done", nil)
	r, _ = loadLoopRun(r.ID)
	if r.Builds != 2 || !r.ReportOnly || r.BuildFails != 0 || !strings.Contains(startedBuilders[len(startedBuilders)-1], "without a complete report") {
		t.Fatalf("after a build with no report: %+v", r)
	}
	loopBuilderDone(r.ID, 2, "still nothing", nil)
	r, _ = loadLoopRun(r.ID)
	if r.Builds != 3 || r.ReportOnly || r.BuildFails != 1 {
		t.Fatalf("after a second build with no report: %+v", r)
	}
	loopBuilderDone(r.ID, 2, stubReport(""), nil) // an old build's result is ignored
	if r2, _ := loadLoopRun(r.ID); r2.Builds != 3 || r2.State != loopBuilding {
		t.Fatalf("a superseded build moved the run: %+v", r2)
	}
	loopBuilderDone(r.ID, 3, "", fmt.Errorf("exit status 1"))
	r, _ = loadLoopRun(r.ID)
	if r.Builds != 4 || r.BuildFails != 2 || !strings.Contains(startedBuilders[len(startedBuilders)-1], "exit status 1") {
		t.Fatalf("after a failed build: %+v", r)
	}

	// Build 4's contractor ended without handing over: the watcher starts build 5.
	fakeContractor(t, contractor{ID: r.Builder, Loop: r.ID, Role: "builder", State: "failed", Result: "its window closed before it finished", Ended: time.Now()})
	buildersResume()
	r, _ = loadLoopRun(r.ID)
	if r.State != loopFailed || r.BuildFails != 3 {
		t.Fatalf("three failures in a row: %+v", r)
	}
	if b, _ := loadBatch(r.Batch); !b.Failed {
		t.Error("the batch did not end with its failed step")
	}
}

// The quiet writer reports once per silence, and again after output resumes and stops.
func TestLoopBuilderQuiet(t *testing.T) {
	var mu sync.Mutex
	told := 0
	q := newQuietWriter(io.Discard, 40*time.Millisecond, func() { mu.Lock(); told++; mu.Unlock() })
	defer q.stop()
	time.Sleep(150 * time.Millisecond)
	_, _ = q.Write([]byte("x"))
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if told != 2 {
		t.Errorf("told %d times, want 2", told)
	}
}

// Enter on a building card selects its builder in the Contractors tab.
func TestLoopBuilderCard(t *testing.T) {
	_, _, r := approvedBatch(t)
	loopBaselineRound(r)
	r, _ = loadLoopRun(r.ID)
	fakeContractor(t, contractor{ID: r.Builder, Loop: r.ID, State: "running"})
	rows := tabRows(time.Now())
	var card tabRow
	for _, row := range rows {
		if row.RunID == r.ID {
			card = row
		}
	}
	if card.Watch != r.Builder || !strings.Contains(card.Detail, "⚙ claude build 1") {
		t.Fatalf("card: %+v (run builder %q)", card, r.Builder)
	}
	m := newTabModel(tabActions{rows: func(time.Time) []tabRow { return []tabRow{card} }, detail: func(tabRow) string { return "details" }, now: time.Now})
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = nm.(tabModel)
	m.col = tabInProgress
	m.selectCard()
	m = tabKey(m, "enter")
	if !m.contractors || m.preview == nil || m.preview.c.ID != r.Builder || m.detail {
		t.Errorf("⏎ on a builder's card: contractors %v, preview %+v, details %v", m.contractors, m.preview, m.detail)
	}
}

// Round 1's findings on step 2: a stop between pr_ready and the commit stands (F1-6); a builder that
// cannot start leaves no builder for the watcher to "resume" (F1-2); a dispute that sends the run to
// the person waits for their decision, which starts the next builder (F1-1); a revised brief stops
// the builder and a fresh one builds the revision, not counted as a failure (F1-7); a deferred
// finding's id can be disputed (F1-8).
func TestLoopBuilderEdges(t *testing.T) {
	t.Run("stop before commit", func(t *testing.T) {
		repo, b, r := approvedBatch(t)
		_ = os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0o644)
		tree, _ := workTreeID(repo)
		r.LastTree = tree
		_ = r.set(loopPRReady, "passed", "pr_ready", nil)
		_ = withBatch(b.ID, func(b *loopBatch) error { b.Stopped = true; return nil })
		head, _ := gitMem(repo, "rev-parse", "HEAD")
		realBuilderAdvance(r.ID)
		if now, _ := gitMem(repo, "rev-parse", "HEAD"); now != head {
			t.Error("a stopped batch's step was committed")
		}
	})
	t.Run("start fails", func(t *testing.T) {
		_, _, r := approvedBatch(t)
		old := startLoopBuilder
		t.Cleanup(func() { startLoopBuilder = old })
		startLoopBuilder = func(*loopRun, string, bool) error { return fmt.Errorf("no tmux") }
		r.Builder = "ab0001"
		_ = r.save(loopEvent{Type: "test"})
		fakeContractor(t, contractor{ID: "ab0001", Loop: r.ID, Role: "builder", State: "done", Ended: time.Now()})
		loopBaselineRound(r)
		r, _ = loadLoopRun(r.ID)
		if r.State != loopBuilding || r.Builder != "" {
			t.Fatalf("after a builder that could not start: %s, builder %q", r.State, r.Builder)
		}
		if msgs := strings.Join(loopTake(r.ID), "\n"); !strings.Contains(msgs, "could not start its builder: no tmux") {
			t.Errorf("the session was not told: %q", msgs)
		}
		buildersResume()
		if r2, _ := loadLoopRun(r.ID); r2.BuildFails != 0 || r2.State != loopBuilding {
			t.Errorf("the watcher took it for a lost builder: %+v", r2)
		}
	})
	t.Run("dispute to the person", func(t *testing.T) {
		_, _, r := approvedBatch(t)
		loopBaselineRound(r)
		r, _ = loadLoopRun(r.ID)
		r.Step, r.Round = 4, 1 // the run that puts deferred findings to the person: disputes go to them
		_ = r.save(loopEvent{Type: "test"})
		writeLoopJSON(r.ID, "findings-1.json", []loopTracked{{ID: "F1-1", loopFinding: loopFinding{Priority: "P1", File: "a.go", Line: 1, Quote: "q", Claim: "c"}}})
		before := len(spawnedRounds)
		loopBuilderDone(r.ID, r.Builds, stubReport("## Disputes\nF1-1: not a bug\n"), nil)
		r, _ = loadLoopRun(r.ID)
		if r.State != loopAwaitYou || len(spawnedRounds) != before {
			t.Fatalf("after a dispute: %s, rounds %v", r.State, spawnedRounds[before:])
		}
		builds := r.Builds
		if err := loopDecideDispute(r.ID, "F1-1", "dismiss"); err != nil {
			t.Fatal(err)
		}
		r, _ = loadLoopRun(r.ID)
		if r.State != loopBuilding || r.Builds != builds+1 || !strings.Contains(startedBuilders[len(startedBuilders)-1], "decided disputed finding F1-1: dismiss") {
			t.Errorf("after the decision: %s, builds %d", r.State, r.Builds)
		}
	})
	t.Run("brief revised", func(t *testing.T) {
		_, _, r := approvedBatch(t)
		loopBaselineRound(r)
		r, _ = loadLoopRun(r.ID)
		stopped := ""
		old := stopContractor
		t.Cleanup(func() { stopContractor = old })
		stopContractor = func(id string) { stopped = id }
		builder := r.Builder
		if _, err := batchReviseStep(r, latestBrief(r)+"\nA clarification.\n"); err != nil {
			t.Fatal(err)
		}
		if stopped != builder {
			t.Errorf("the builder at work was not stopped (stopped %q)", stopped)
		}
		r, _ = loadLoopRun(r.ID)
		// However the scope review ends, the step is building again with a new revision.
		r.Revision++
		_ = r.set(loopBuilding, "revision accepted", "test", nil)
		fakeContractor(t, contractor{ID: builder, Loop: r.ID, Role: "builder", State: "failed", Ended: time.Now()})
		buildersResume()
		r, _ = loadLoopRun(r.ID)
		if r.Builds != 2 || r.BuildFails != 0 || !strings.Contains(startedBuilders[len(startedBuilders)-1], "brief was revised") {
			t.Errorf("after the revision: %+v", r)
		}
	})
	t.Run("retry cannot start", func(t *testing.T) {
		_, _, r := approvedBatch(t)
		loopBaselineRound(r)
		r, _ = loadLoopRun(r.ID)
		old := startLoopBuilder
		t.Cleanup(func() { startLoopBuilder = old })
		startLoopBuilder = func(*loopRun, string, bool) error { return fmt.Errorf("no tmux") }
		loopBuilderDone(r.ID, r.Builds, "", fmt.Errorf("exit status 1"))
		if r, _ = loadLoopRun(r.ID); r.Builder != "" || r.BuildFails != 1 {
			t.Fatalf("after a retry that could not start: builder %q, fails %d", r.Builder, r.BuildFails)
		}
		buildersResume()
		if r2, _ := loadLoopRun(r.ID); r2.BuildFails != 1 {
			t.Errorf("the watcher counted the same build again: %d", r2.BuildFails)
		}
	})
	t.Run("wrong branch", func(t *testing.T) {
		repo, _, r := approvedBatch(t)
		_ = exec.Command("git", "-C", repo, "checkout", "-q", "-b", "elsewhere").Run()
		if err := realStartLoopBuilder(r, "build", false); err == nil || !strings.Contains(err.Error(), "elsewhere is checked out") {
			t.Errorf("a builder started on another branch: %v", err)
		}
	})
	t.Run("checks without credentials", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "gho_x")
		t.Setenv("SSH_AUTH_SOCK", "/tmp/agent")
		repo, _, r := approvedBatch(t)
		probe := []string{`test -z "$GH_TOKEN" && test -z "$SSH_AUTH_SOCK"`}
		if c := runLoopChecksFor(r, probe, filepath.Join(t.TempDir(), "log")).Checks[0]; c.Exit != 0 {
			t.Errorf("a builder-driven step's checks see the person's credentials")
		}
		single := testLoopRun(t, repo)
		if c := runLoopChecksFor(single, []string{`test -n "$GH_TOKEN"`}, filepath.Join(t.TempDir(), "log")).Checks[0]; c.Exit != 0 {
			t.Errorf("a session-built run's checks lost the person's environment")
		}
	})
	t.Run("git config cannot run code", func(t *testing.T) {
		repo, _ := loopRepo(t)
		marker := filepath.Join(t.TempDir(), "ran")
		hook := filepath.Join(t.TempDir(), "fsmonitor")
		_ = os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
		_ = exec.Command("git", "-C", repo, "config", "core.fsmonitor", hook).Run()
		_ = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644)
		if _, err := workTreeID(repo); err != nil {
			t.Fatal(err)
		}
		if _, err := gitLoop(repo, "status", "--porcelain"); err != nil {
			t.Fatal(err)
		}
		if fileExists(marker) {
			t.Error("partyline's git ran the checkout's fsmonitor program")
		}
	})
	t.Run("deferred ids", func(t *testing.T) {
		if d := builderDisputes("## Disputes\n- S1-F2-3: already handled\nF1-1: no\n"); len(d) != 2 || d[0].id != "S1-F2-3" {
			t.Errorf("disputes %+v", d)
		}
	})
}
