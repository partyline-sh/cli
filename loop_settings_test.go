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
	t.Run("paths and paste", testLoopSettingsPaths)
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

// A quality file typed as a relative path is saved absolute, so a batch started elsewhere finds it;
// a hand-edited relative path is refused. A paste reaches the field. The approval document shows the
// standard's text. Long values do not wrap the popup.
func testLoopSettingsPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "q.md"), []byte("Line one of ours.\nLine two.\n"), 0o600)
	t.Chdir(dir)
	m := newSettingsModel(loopSettings{}, saveSettings)
	m.sel = 3
	m, _ = settingsKey(m, "enter")
	m = typeIn(m, "q.md")
	m, _ = settingsKey(m, "enter")
	got, _ := loadSettings()
	if got.Quality != filepath.Join(dir, "q.md") && !strings.HasSuffix(got.Quality, "/q.md") || !filepath.IsAbs(got.Quality) {
		t.Fatalf("saved quality path %q (err %q)", got.Quality, m.err)
	}
	t.Chdir(t.TempDir())
	c, err := startConfig(loopFlags{review: "claude,codex"})
	if err != nil || c.Quality != "Line one of ours.\nLine two." {
		t.Errorf("a batch started elsewhere: %+v %v", c, err)
	}
	if d := configDescription(c); !strings.Contains(d, "> Line one of ours.") || !strings.Contains(d, "> Line two.") {
		t.Errorf("the approval document does not show the standard:\n%s", d)
	}
	if err := saveSettings(loopSettings{Quality: "q.md"}); err == nil {
		t.Error("a relative quality path was saved")
	}

	m.sel = 0
	m, _ = settingsKey(m, "enter")
	m.in.SetValue("")
	nm, _ := m.Update(tea.PasteMsg{Content: "claude:opus, codex:gpt-5"})
	m = nm.(settingsModel)
	m, _ = settingsKey(m, "enter")
	if got, _ := loadSettings(); len(got.Builder) != 2 || got.Builder[1] != "codex:gpt-5" {
		t.Errorf("a pasted builder list was not saved: %+v (err %q)", got.Builder, m.err)
	}

	m.s.Quality = "/" + strings.Repeat("long/", 40) + "q.md"
	m.sel = 3 // editing the long value itself
	m, _ = settingsKey(m, "enter")
	nm, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 22})
	view := gateANSI.ReplaceAllString(nm.(settingsModel).View().Content, "")
	for _, line := range strings.Split(view, "\n") {
		if w := len([]rune(line)); w > 60 {
			t.Errorf("a line is %d columns in a 60-column popup: %q", w, line)
		}
	}
	if n := strings.Count(view, "\n") + 1; n > 22 {
		t.Errorf("the popup is %d rows in 22", n)
	}
}

// Each row saves to ~/.partyline/settings.json and loads back; a bad value is refused with the reason
// and nothing is saved.
func testLoopSettingsRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	quality := filepath.Join(t.TempDir(), "quality.md")
	_ = os.WriteFile(quality, []byte("Our standard: tests for every change.\n"), 0o600)
	m := newSettingsModel(loopSettings{}, saveSettings)
	edit := func(row int, text string) {
		t.Helper()
		m.sel = row
		m, _ = settingsKey(m, "enter")
		if !m.edit {
			t.Fatalf("⏎ on %s did not start editing", settingsRows[row].label)
		}
		m = typeIn(m, text)
		m, _ = settingsKey(m, "enter")
	}
	edit(0, "claude:opus, codex:gpt-5")
	edit(1, "codex:gpt-5, claude:opus; claude:sonnet")
	m.sel = 2
	m, _ = settingsKey(m, "enter")
	edit(3, quality)
	m.sel = 4
	m, _ = settingsKey(m, "space")
	edit(5, "codex:gpt-5")
	if m.err != "" || m.edit {
		t.Fatalf("after the edits: err %q, editing %v", m.err, m.edit)
	}
	got, err := loadSettings()
	want := loopSettings{Builder: []string{"claude:opus", "codex:gpt-5"}, Reviewers: [][]string{{"codex:gpt-5", "claude:opus"}, {"claude:sonnet"}},
		NoFailover: true, Quality: quality, Disputes: "arbiter", Arbiter: "codex:gpt-5"}
	if err != nil || !slices.Equal(got.Builder, want.Builder) || len(got.Reviewers) != 2 || !slices.Equal(got.Reviewers[0], want.Reviewers[0]) ||
		got.NoFailover != want.NoFailover || got.Quality != want.Quality || got.Disputes != want.Disputes || got.Arbiter != want.Arbiter {
		t.Fatalf("saved %+v, %v; want %+v", got, err, want)
	}
	for _, row := range []int{0, 1, 3, 5} {
		if v := settingsShown(got, row); strings.Contains(v, "(") && row != 1 {
			t.Errorf("row %s shows a default for a set value: %q", settingsRows[row].label, v)
		}
	}

	for row, bad := range map[int]string{0: "gemini:pro", 1: "claude:", 3: "/no/such/file", 5: "codex:a b"} {
		edit(row, bad)
		if m.err == "" || !m.edit {
			t.Errorf("%s %q was accepted", settingsRows[row].label, bad)
		}
		m, _ = settingsKey(m, "esc")
		if after, _ := loadSettings(); after.Arbiter != "codex:gpt-5" || after.Quality != quality || len(after.Builder) != 2 {
			t.Errorf("a refused %s changed the saved settings: %+v", settingsRows[row].label, after)
		}
	}
	// Empty values are the defaults again.
	edit(0, "")
	edit(3, "built in")
	m.sel = 4
	m, _ = settingsKey(m, "enter")
	if got, _ := loadSettings(); len(got.Builder) != 0 || got.Quality != "" || got.Disputes != "" {
		t.Errorf("defaults not restored: %+v", got)
	}
	if v := settingsShown(loopSettings{}, 0); !strings.Contains(v, "the engine") {
		t.Errorf("default builder shown as %q", v)
	}
}

// The key table: what each key does on the list and while editing.
func testLoopSettingsKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saved := 0
	m := newSettingsModel(loopSettings{}, func(loopSettings) error { saved++; return nil })
	for _, tc := range []struct {
		keys  []string
		sel   int
		edit  bool
		quit  bool
		saves int
	}{
		{[]string{"up"}, 0, false, false, 0},
		{[]string{"down", "j"}, 2, false, false, 0},
		{[]string{"enter"}, 2, false, false, 1},                                        // Failover toggles and saves
		{[]string{"space"}, 2, false, false, 2},                                        // and back
		{[]string{"k", "enter"}, 1, true, false, 2},                                    // Reviewers edits
		{[]string{"x"}, 1, true, false, 2},                                             // typed into the field
		{[]string{"esc"}, 1, false, false, 2},                                          // cancel, nothing saved
		{[]string{"down", "down", "down", "down", "down", "down"}, 5, false, false, 2}, // stops at the last row
		{[]string{"esc"}, 5, false, true, 2},
		{[]string{"q"}, 5, false, true, 2},
	} {
		var cmd tea.Cmd
		m, cmd = settingsKey(m, tc.keys...)
		if m.sel != tc.sel || m.edit != tc.edit || quits(cmd) != tc.quit || saved != tc.saves {
			t.Errorf("%v: sel %d edit %v quit %v saves %d; want %d %v %v %d", tc.keys, m.sel, m.edit, quits(cmd), saved, tc.sel, tc.edit, tc.quit, tc.saves)
		}
	}
	if !strings.Contains(m.View().Content, "Builder") || !strings.Contains(m.View().Content, "esc") {
		t.Error("the popup does not show its rows and keys")
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
