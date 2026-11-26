package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func settingsKey(m settingsModel, keys ...string) (settingsModel, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "ctrl+u":
			msg = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		nm, c := m.Update(msg)
		m, cmd = nm.(settingsModel), c
	}
	return m, cmd
}

// typeIn replaces the edited value with text.
func typeIn(m settingsModel, text string) settingsModel {
	m.in.SetValue("")
	for _, r := range text {
		m, _ = settingsKey(m, string(r))
	}
	return m
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestLoopSettings(t *testing.T) {
	t.Run("rows", testLoopSettingsRows)
	t.Run("keys", testLoopSettingsKeys)
	t.Run("batch keeps", testLoopSettingsBatchKeeps)
	t.Run("batch start", testLoopSettingsBatchStart)
	t.Run("quality path", testLoopSettingsPaths)
}

// batch start takes --config's config, none when --review names the reviewers, else the settings.
func testLoopSettingsBatchStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = saveSettings(loopSettings{Builder: []string{"codex:gpt-5"}})
	if c, err := startConfig(loopFlags{review: "claude,codex"}); err != nil || c == nil || string(c.Builder) != `["codex:gpt-5"]` {
		t.Errorf("no flags: %+v %v", c, err)
	}
	if c, err := startConfig(loopFlags{review: "codex", reviewSet: true}); err != nil || c != nil {
		t.Errorf("--review: %+v %v", c, err)
	}
	own := &loopConfig{Reviewers: [][]string{{"claude:opus"}}}
	if c, err := startConfig(loopFlags{review: "claude,codex", config: own}); err != nil || c != own {
		t.Errorf("--config: %+v %v", c, err)
	}
	_ = os.WriteFile(settingsPath(), []byte(`{"quality":"relative/q.md"}`), 0o600) // hand-edited
	if _, err := startConfig(loopFlags{review: "claude,codex"}); err == nil || !strings.Contains(err.Error(), "full path") {
		t.Errorf("a hand-edited relative quality path: %v", err)
	}
	_ = os.WriteFile(settingsPath(), []byte(`{"builder":["gemini"]}`), 0o600)
	if _, err := startConfig(loopFlags{review: "claude,codex"}); err == nil || !strings.Contains(err.Error(), "loop settings") {
		t.Errorf("bad settings: %v", err)
	}
}

// A batch started from the settings carries them in its config, which the approval document shows;
// changing the settings or the quality file later changes nothing about the approved batch or its
// later steps.
func testLoopSettingsBatchKeeps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	quality := filepath.Join(t.TempDir(), "quality.md")
	_ = os.WriteFile(quality, []byte("Standard A.\n"), 0o600)
	s := loopSettings{Builder: []string{"codex:gpt-5"}, Reviewers: [][]string{{"claude:opus", "codex:gpt-5"}}, Quality: quality, Disputes: "arbiter", Arbiter: "claude:opus"}
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	loaded, _ := loadSettings()
	config, err := batchConfigFromSettings(loaded, "claude,codex")
	if err != nil {
		t.Fatal(err)
	}
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "Do one."), batchBrief("Two", "Do two.")}, config)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	doc := latestBrief(r)
	for _, want := range []string{"slot 1: claude:opus -> codex:gpt-5", "failover: true", "builder: codex:gpt-5", "quality standard: " + quality + " (sha256", "disputes: arbiter claude:opus"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the approval document lacks %q:\n%s", want, doc)
		}
	}
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	// The person changes everything afterwards.
	_ = saveSettings(loopSettings{Builder: []string{"claude:haiku"}, NoFailover: true})
	_ = os.WriteFile(quality, []byte("Standard B.\n"), 0o600)

	r, _ = loadLoopRun(r.ID)
	b, _ = loadBatch(b.ID)
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	next, err := batchNextStep(r)
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := loadLoopRun(next)
	if err := r2.checkConfig(); err != nil {
		t.Errorf("step 2's config is not the approved one: %v", err)
	}
	if e := batchBuilders(b); len(e) != 1 || e[0].String() != "codex:gpt-5" {
		t.Errorf("step 2's builder %v, want the approved codex:gpt-5", e)
	}
	if q := qualityStandard(r2.Config); q != "Standard A." || !r2.Config.Failover || r2.Config.Arbiter != "claude:opus" {
		t.Errorf("step 2 holds the work to %q, failover %v, arbiter %q", q, r2.Config.Failover, r2.Config.Arbiter)
	}
	if p := builderPrompt(latestBrief(r2), qualityStandard(r2.Config), "build"); !strings.Contains(p, "Standard A.") || strings.Contains(p, "Work is held to six levels") {
		t.Error("the builder's prompt does not carry the batch's quality standard")
	}
	if p := loopReviewPromptWith("brief", "report", "diff", qualityStandard(r2.Config)); !strings.Contains(p, "Standard A.") {
		t.Error("the review prompt does not carry the batch's quality standard")
	}

	// With no settings saved, a batch gets the reviewers it always did and the built-in standard.
	t.Setenv("HOME", t.TempDir())
	none, _ := loadSettings()
	c, err := batchConfigFromSettings(none, "claude,codex")
	if err != nil || len(c.Reviewers) != 2 || c.Reviewers[0][0] != "claude" || c.Reviewers[1][0] != "codex" || !c.Failover || qualityStandard(c) != loopQualityStandard {
		t.Errorf("defaults: %+v %v", c, err)
	}
	if f, _ := parseLoopFlags([]string{"--review", "codex"}); !f.reviewSet {
		t.Error("--review is not recorded as given")
	}
}

// pickAI opens the picker on the selected row and chooses the engine, model and effort at those
// places in the lists.
func pickAI(t *testing.T, m settingsModel, open string, engine, model, effort int) settingsModel {
	t.Helper()
	m, _ = settingsKey(m, open)
	if !m.pick {
		t.Fatalf("%s did not open the picker", open)
	}
	for _, steps := range []int{engine, model, effort} {
		m.at[m.col] = 0
		for i := 0; i < steps; i++ {
			m, _ = settingsKey(m, "down")
		}
		m, _ = settingsKey(m, "enter")
	}
	if m.pick {
		t.Fatal("the picker stayed open after the effort")
	}
	return m
}

// Every AI is chosen from lists (engine, model, effort), with backups added and removed by key;
// reviewers are added and removed; switches flip; everything saves and loads back.
func testLoopSettingsRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // codex has no cached list here: gpt-5 with the fallback efforts
	m := newSettingsModel(loopSettings{}, saveSettings)
	if v := gateANSI.ReplaceAllString(m.View().Content, ""); !strings.Contains(v, "choose…") || !strings.Contains(v, "Still needed: builder, reviewer 1") {
		t.Errorf("first run:\n%s", v)
	}
	m.sel = 0
	m = pickAI(t, m, "enter", 1, 0, 1) // codex · gpt-5 · low
	m = pickAI(t, m, "b", 0, 1, 2)     // backup: claude · opus · high
	m.sel = 1
	m = pickAI(t, m, "enter", 0, 2, 1) // reviewer 1: claude · sonnet · medium
	// + Add a reviewer: cancelling leaves no empty reviewer; choosing saves it.
	m.sel = 2
	m, _ = settingsKey(m, "enter", "esc")
	if len(m.s.Reviewers) != 1 {
		t.Fatalf("a cancelled reviewer stayed: %+v", m.s.Reviewers)
	}
	m.sel = 2
	m = pickAI(t, m, "enter", 1, 0, 3) // reviewer 2: codex · gpt-5 · high
	got, _ := loadSettings()
	if !slices.Equal(got.Builder, []string{"codex:gpt-5:low", "claude:opus:high"}) || len(got.Reviewers) != 2 ||
		got.Reviewers[0][0] != "claude:sonnet:medium" || got.Reviewers[1][0] != "codex:gpt-5:high" {
		t.Fatalf("saved %+v (err %q)", got, m.err)
	}
	if v := settingsShown(got, settingsRow{kind: rowAI, role: "builder"}); v != "codex · gpt-5 · low  (+1 backup)" {
		t.Errorf("builder shown as %q", v)
	}
	// x removes the backup, then reviewer 2 itself.
	m.sel = 0
	m, _ = settingsKey(m, "x")
	m.sel = 2
	m, _ = settingsKey(m, "x")
	if got, _ = loadSettings(); len(got.Builder) != 1 || len(got.Reviewers) != 1 {
		t.Fatalf("x did not remove: %+v", got)
	}
	// Loops decide by default: the arbiter row is there, optional, defaulting to reviewer 1.
	find := func(k settingsRowKind) int {
		return slices.IndexFunc(m.rows(), func(r settingsRow) bool { return r.kind == k })
	}
	m.sel = find(rowFailover)
	m, _ = settingsKey(m, "enter")
	if v := settingsShown(m.s, settingsRow{kind: rowAI, role: "arbiter"}); v != "reviewer 1 (default)" || m.s.notChosen() != "" {
		t.Errorf("arbiter by default: %q, still needed %q", v, m.s.notChosen())
	}
	m.sel = slices.IndexFunc(m.rows(), func(r settingsRow) bool { return r.role == "arbiter" })
	m = pickAI(t, m, "enter", 0, 0, 4) // arbiter: claude · fable · max
	if got, _ = loadSettings(); !got.NoFailover || got.Arbiter != "claude:fable:max" || got.AskMe || got.notChosen() != "" {
		t.Fatalf("switches and arbiter: %+v", got)
	}
	if v := gateANSI.ReplaceAllString(m.View().Content, ""); !strings.Contains(v, "▸ Arbiter") || !strings.Contains(v, "loops decide") {
		t.Errorf("selection marker or decisions row:\n%s", v)
	}
	// Asking the person brings back the disputes row.
	m.sel = find(rowDecisions)
	m, _ = settingsKey(m, "enter")
	if got, _ = loadSettings(); !got.AskMe || find(rowDisputes) < 0 {
		t.Fatalf("ask me: %+v", got)
	}
	// The Jev key is typed masked, saved privately, never shown, and removed with x.
	m.sel = find(rowJev)
	m, _ = settingsKey(m, "enter")
	m = typeIn(m, "jev-secret-123")
	if strings.Contains(m.View().Content, "jev-secret-123") {
		t.Error("the Jev key is shown while typed")
	}
	m, _ = settingsKey(m, "enter")
	if savedJevKey() != "jev-secret-123" || strings.Contains(m.View().Content, "jev-secret-123") {
		t.Errorf("Jev key: saved %q", savedJevKey())
	}
	if info, err := os.Stat(jevKeyPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("Jev key file: %v %v", info, err)
	}
	m, _ = settingsKey(m, "x")
	if savedJevKey() != "" {
		t.Error("x did not remove the Jev key")
	}
}

// The key table on the list.
func testLoopSettingsKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saved := 0
	m := newSettingsModel(loopSettings{}, func(loopSettings) error { saved++; return nil })
	for _, tc := range []struct {
		keys  []string
		sel   int
		pick  bool
		quit  bool
		saves int
	}{
		{[]string{"up"}, 0, false, false, 0},
		{[]string{"down", "j", "j"}, 3, false, false, 0},                                               // Failover
		{[]string{"enter"}, 3, false, false, 1},                                                        // toggles and saves
		{[]string{"k", "k", "enter"}, 1, true, false, 1},                                               // a reviewer opens the picker
		{[]string{"esc"}, 1, false, false, 1},                                                          // cancel, nothing saved
		{[]string{"down", "down", "down", "down", "down", "down", "down", "down"}, 7, false, false, 1}, // stops at the last row (the Jev key)
		{[]string{"esc"}, 7, false, true, 1},
		{[]string{"q"}, 7, false, true, 1},
	} {
		var cmd tea.Cmd
		m, cmd = settingsKey(m, tc.keys...)
		if m.sel != tc.sel || m.pick != tc.pick || quits(cmd) != tc.quit || saved != tc.saves {
			t.Errorf("%v: sel %d pick %v quit %v saves %d; want %d %v %v %d", tc.keys, m.sel, m.pick, quits(cmd), saved, tc.sel, tc.pick, tc.quit, tc.saves)
		}
	}
}

// A quality file typed as a relative path is saved absolute, so a batch started elsewhere finds it;
// a missing file is refused. The approval document shows the standard's text. Long values and the
// picker do not overflow the popup.
func testLoopSettingsPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "q.md"), []byte("Line one of ours.\nLine two.\n"), 0o600)
	t.Chdir(dir)
	m := newSettingsModel(loopSettings{}, saveSettings)
	m.sel = slices.IndexFunc(m.rows(), func(r settingsRow) bool { return r.kind == rowQuality })
	m, _ = settingsKey(m, "enter")
	m = typeIn(m, "q.md")
	m, _ = settingsKey(m, "enter")
	got, _ := loadSettings()
	if !filepath.IsAbs(got.Quality) || !strings.HasSuffix(got.Quality, "/q.md") {
		t.Fatalf("saved quality path %q (err %q)", got.Quality, m.err)
	}
	t.Chdir(t.TempDir())
	c, err := startConfig(loopFlags{review: "claude,codex"})
	if err != nil || c.Quality != "Line one of ours.\nLine two." {
		t.Errorf("a batch started elsewhere: %+v %v", c, err)
	}
	if d := configDescription(c); !strings.Contains(d, "> Line one of ours.") {
		t.Errorf("the approval document does not show the standard:\n%s", d)
	}
	m, _ = settingsKey(m, "enter")
	m = typeIn(m, "/no/such/file")
	m, _ = settingsKey(m, "enter")
	if m.err == "" {
		t.Error("a missing quality file was accepted")
	}
	m, _ = settingsKey(m, "esc")
	m.s.Quality = "/" + strings.Repeat("long/", 40) + "q.md"
	m.sel = 0
	m, _ = settingsKey(m, "enter") // the picker open
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	view := gateANSI.ReplaceAllString(nm.(settingsModel).View().Content, "")
	for _, line := range strings.Split(view, "\n") {
		if w := len([]rune(line)); w > 70 {
			t.Errorf("a line is %d columns in a 70-column popup: %q", w, line)
		}
	}
	if n := strings.Count(view, "\n") + 1; n > 30 {
		t.Errorf("the popup is %d rows in 30", n)
	}
}
