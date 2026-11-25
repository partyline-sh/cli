package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Loop settings: who builds and reviews new batches, whether they fail over, the quality standard and
// how disputes are decided, chosen once in the settings popup (ctrl-\ ,) and kept in
// ~/.partyline/settings.json. A batch copies them into its config when it starts, so the person
// approves them with the batch and changing them later never changes a batch already approved.

type loopSettings struct {
	Builder    []string   `json:"builder,omitempty"`   // engine[:model], first choice then fallbacks
	Reviewers  [][]string `json:"reviewers,omitempty"` // slots, each an ordered list
	NoFailover bool       `json:"no_failover,omitempty"`
	Quality    string     `json:"quality,omitempty"`  // a file holding the standard; "" is the built-in one
	Disputes   string     `json:"disputes,omitempty"` // "" (ask me) or "arbiter"
	Arbiter    string     `json:"arbiter,omitempty"`  // engine:model, when Disputes is "arbiter"
	AskMe      bool       `json:"ask_me,omitempty"`   // the person approves briefs and rules on disputes; off, loops decide (loop_decide.go)
}

func settingsPath() string { return filepath.Join(stateDir(), "settings.json") }

// loadSettings reads the settings; none saved is the defaults.
func loadSettings() (loopSettings, error) {
	var s loopSettings
	b, err := os.ReadFile(settingsPath())
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %v", settingsPath(), err)
	}
	return s, nil
}

func saveSettings(s loopSettings) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath())
}

func (s loopSettings) validate() error {
	for _, b := range s.Builder {
		if err := checkEngineModel("builder", b); err != nil {
			return err
		}
	}
	for _, slot := range s.Reviewers {
		for _, m := range slot {
			if err := checkEngineModel("reviewer", m); err != nil {
				return err
			}
		}
	}
	if s.Quality != "" {
		if !filepath.IsAbs(expandHome(s.Quality)) {
			return fmt.Errorf("quality standard %s: use a full path; the settings apply in every directory", s.Quality)
		}
		if _, err := readQuality(s.Quality); err != nil {
			return err
		}
	}
	switch s.Disputes {
	case "":
	case "arbiter":
		// Not chosen yet is allowed here: notChosen holds loops back until it is.
		if s.Arbiter != "" {
			if err := checkEngineModel("arbiter", s.Arbiter); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("disputes must be ask me or arbiter")
	}
	return nil
}

// readQuality reads a quality standard file: text, under 64 KB.
func readQuality(path string) (string, error) {
	b, err := os.ReadFile(expandHome(path))
	if err != nil {
		return "", fmt.Errorf("quality standard: %v", err)
	}
	text := strings.TrimSpace(string(b))
	if text == "" || len(b) > 64<<10 || termSafeLines(text) != text {
		return "", fmt.Errorf("quality standard %s must be plain text under 64 KB", path)
	}
	return text, nil
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// batchConfigFromSettings is a new batch's config: the settings, with review (the --review default)
// as the reviewers when none are set. The quality standard's text is copied in, so a later edit of
// its file does not change an approved batch.
func batchConfigFromSettings(s loopSettings, review string) (*loopConfig, error) {
	c := &loopConfig{Reviewers: s.Reviewers, Failover: !s.NoFailover, Disputes: "ask", QualitySource: "built in"}
	if len(c.Reviewers) == 0 {
		engines, err := parseLoopEngines(review)
		if err != nil {
			return nil, err
		}
		for _, e := range engines {
			c.Reviewers = append(c.Reviewers, []string{e.String()})
		}
	}
	if len(s.Builder) > 0 {
		c.Builder, _ = json.Marshal(s.Builder)
	}
	if s.Quality != "" {
		text, err := readQuality(s.Quality)
		if err != nil {
			return nil, err
		}
		c.Quality, c.QualitySource = text, s.Quality
	}
	if s.Disputes == "arbiter" {
		c.Disputes, c.Arbiter = "arbiter", s.Arbiter
	}
	return c, c.validate()
}

// qualityStandard is the standard a run is held to: its batch's, else the built-in one.
func qualityStandard(c *loopConfig) string {
	if c != nil && c.Quality != "" {
		return c.Quality
	}
	return loopQualityStandard
}

// notChosen says what the person has still to choose before any loop runs, or "" when nothing:
// partyline picks no model or effort for them, since either can spend far more than they meant to.
func (s loopSettings) notChosen() string {
	var missing []string
	need := func(role string, specs []string) {
		if len(specs) == 0 || specs[0] == "" {
			missing = append(missing, role)
			return
		}
		for _, spec := range specs {
			name, rest, _ := strings.Cut(spec, ":")
			model, effort, _ := strings.Cut(rest, ":")
			if model == "" || effort == "" {
				missing = append(missing, fmt.Sprintf("%s (%s)", role, name))
				return
			}
		}
	}
	need("builder", s.Builder)
	if len(s.Reviewers) == 0 {
		missing = append(missing, "reviewer 1")
	}
	for i, slot := range s.Reviewers {
		need(fmt.Sprintf("reviewer %d", i+1), slot)
	}
	if s.Disputes == "arbiter" {
		need("arbiter", []string{s.Arbiter})
	}
	return strings.Join(missing, ", ")
}
