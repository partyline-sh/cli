package main

import (
	"fmt"
)

// A loop or batch started before the person has chosen their loop settings does not fail: it is
// recorded, paused from pausedForSettings, and the settings popup opens for the person at once.
// Once the settings are chosen it carries on by itself: a single loop to approval, a batch to its
// reviewers (who are the ones just chosen).

const pausedForSettings = "loop_settings"

// askForSettings is what the person has still to choose, "" when nothing; when something is
// missing it opens the settings popup on their screen straight away.
func askForSettings() string {
	s, err := loadSettings()
	what := s.notChosen()
	if err != nil {
		what = err.Error()
	}
	if what != "" && insidePtlnTmux() {
		_ = tmuxCmd("run-shell", "-b", shQuote(selfExe())+" tmux --settings-popup").Run()
	}
	return what
}

// waitForSettings pauses r until the settings are chosen; fromSettings when its config is to come
// from them (no --config or --review).
func waitForSettings(r *loopRun, fromSettings bool, what string) error {
	r.PausedFrom, r.SettingsConfig = pausedForSettings, fromSettings
	return r.set(loopPaused, "Choose settings to start: "+what, "waiting_for_settings", map[string]any{"missing": what})
}

func settingsWaitAdvice(r *loopRun, what string) string {
	return fmt.Sprintf("Loop %s is saved and waiting for the person to choose settings: %s. Settings are open on their screen. The loop resumes automatically after they choose. Tell the person, run `ptln loop listen %s` in the background, and end your turn. Do not change the settings yourself. The person chooses the builder, reviewers, models, and effort.", r.ID, termSafe(what), r.ID)
}

// waitingForSettings: r is paused until the person chooses their settings.
func waitingForSettings(r *loopRun) bool {
	return r != nil && r.State == loopPaused && r.PausedFrom == pausedForSettings
}

// resumeFromSettings carries a run on once the settings are chosen; called under its run lock.
func resumeFromSettings(r *loopRun) error {
	s, err := loadSettings()
	if err != nil {
		return err
	}
	if what := s.notChosen(); what != "" {
		return fmt.Errorf("loop %s still waits for the loop settings: %s", r.ID, what)
	}
	if r.SettingsConfig {
		c, err := startConfig(loopFlags{review: r.Review})
		if err != nil {
			return err
		}
		r.Config, r.SettingsConfig = c, false
		if r.Batch != "" {
			if err := withBatch(r.Batch, func(b *loopBatch) error { b.Config = c; return nil }); err != nil {
				return err
			}
		}
	}
	r.PausedFrom = ""
	if r.Batch == "" {
		return r.set(loopAwaiting, "brief 1 waiting for approval", "settings_chosen", nil)
	}
	if err := r.set(loopBatchRev, "the reviewers are reading the batch", "batch_started", map[string]any{"batch": r.Batch}); err != nil {
		return err
	}
	return spawnBatchReview(r.ID)
}

// resumeSettingsWaits carries on every run that waited for settings, once they are chosen: when the
// settings popup closes, and on the watcher's tick.
func resumeSettingsWaits() {
	s, err := loadSettings()
	if err != nil || s.notChosen() != "" {
		return
	}
	for _, r := range loopRuns() {
		if waitingForSettings(r) {
			_ = loopResumeRun(r.ID, "settings chosen")
		}
	}
}
