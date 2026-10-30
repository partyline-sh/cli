package main

import (
	"slices"
	"strings"
	"testing"
)

// No loop starts until the person has chosen a model and effort for every builder, reviewer and
// arbiter; partyline chooses none of them.
func TestSettingsNotChosen(t *testing.T) {
	chosen := loopSettings{Builder: []string{"claude:sonnet:medium"}, Reviewers: [][]string{{"codex:gpt-5:low"}, {"claude:sonnet:high"}}}
	if what := chosen.notChosen(); what != "" {
		t.Errorf("chosen settings reported missing %q", what)
	}
	for name, s := range map[string]loopSettings{
		"nothing saved":     {},
		"no reviewers":      {Builder: chosen.Builder},
		"builder no effort": {Builder: []string{"claude:opus"}, Reviewers: chosen.Reviewers},
		"bare reviewer":     {Builder: chosen.Builder, Reviewers: [][]string{{"codex"}}},
		"arbiter bare":      {Builder: chosen.Builder, Reviewers: chosen.Reviewers, Disputes: "arbiter", Arbiter: "claude"},
	} {
		if s.notChosen() == "" {
			t.Errorf("%s: counted as chosen", name)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX", "") // no popup from a test
	if askForSettings() == "" {
		t.Error("nothing saved counted as chosen")
	}

	// Started before the settings: recorded and waiting, not refused; carries on once they are chosen.
	repo := t.TempDir()
	single := testLoopRun(t, repo)
	if err := waitForSettings(single, true, "the builder"); err != nil {
		t.Fatal(err)
	}
	_, step, err := recordBatch(t.TempDir(), []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForSettings(step, true, "the builder"); err != nil {
		t.Fatal(err)
	}
	if err := loopResumeRun(single.ID, "by hand"); err == nil {
		t.Error("a loop resumed before the settings were chosen")
	}
	resumeSettingsWaits()
	if r, _ := loadLoopRun(single.ID); !waitingForSettings(r) {
		t.Errorf("carried on without settings: %s", r.State)
	}
	if err := saveSettings(chosen); err != nil {
		t.Fatal(err)
	}
	resumeSettingsWaits()
	r, _ := loadLoopRun(single.ID)
	if r.State != loopAwaiting || r.Config == nil || len(r.Config.Reviewers) != 2 || r.SettingsConfig {
		t.Errorf("single loop after settings: %s %+v", r.State, r.Config)
	}
	r, _ = loadLoopRun(step.ID)
	b, _ := loadBatch(step.Batch)
	if r.State != loopBatchRev || b.Config == nil || b.Config.Reviewers[0][0] != "codex:gpt-5:low" {
		t.Errorf("batch after settings: %s %+v", r.State, b.Config)
	}
}

// engine:model:effort reaches each engine as its own flag, and an effort the engine lacks is refused.
func TestEngineEffort(t *testing.T) {
	es, err := parseLoopEngines("claude:opus:high, codex:gpt-5:minimal, codex")
	if err != nil {
		t.Fatal(err)
	}
	if es[0].String() != "claude:opus:high" || es[2].String() != "codex" {
		t.Errorf("round trip: %v", es)
	}
	if a := es[0].claudeArgs(); !slices.Equal(a, []string{"--model", "opus", "--effort", "high"}) {
		t.Errorf("claude args %v", a)
	}
	if a := es[1].codexArgs(); !slices.Equal(a, []string{"-m", "gpt-5", "-c", "model_reasoning_effort=minimal"}) {
		t.Errorf("codex args %v", a)
	}
	if a := strings.Join(codexBuildArgs(es[1], "/d", "/l"), " "); !strings.Contains(a, "model_reasoning_effort=minimal") {
		t.Errorf("codex builder argv %s", a)
	}
	for _, bad := range []string{"claude:opus:minimal", "codex:gpt-5:max"} {
		if _, err := parseLoopEngines(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
		if err := checkEngineModel("builder", bad); err == nil {
			t.Errorf("settings accepted %s", bad)
		}
	}
}
