package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// A batch's model config (`ptln loop batch start --config <file>`): reviewer slots, each an ordered
// list of engine:model to fail over through, and whether to fail over at all. It is written into the
// batch document the person approves, so its hash is part of what they approved.

type loopConfig struct {
	Reviewers [][]string      `json:"reviewers"`
	Failover  bool            `json:"failover"`
	Builder   json.RawMessage `json:"builder,omitempty"`
	// From the settings popup: the quality standard's text when it is not the built-in one, where it
	// came from, and how disputes are decided.
	Quality       string `json:"quality,omitempty"`
	QualitySource string `json:"quality_source,omitempty"`
	Disputes      string `json:"disputes,omitempty"` // "", "ask" or "arbiter"
	Arbiter       string `json:"arbiter,omitempty"`
}

// readLoopConfig reads and checks a config file; failover defaults to on.
func readLoopConfig(path string) (*loopConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &loopConfig{Failover: true}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *loopConfig) validate() error {
	if len(c.Reviewers) == 0 {
		return fmt.Errorf("config: no reviewer slots")
	}
	// The builders build every step: one engine:model, or a list in the order failover tries them.
	if len(c.Builder) > 0 {
		builders := configBuilders(c)
		if len(builders) == 0 {
			return fmt.Errorf("config: builder must be an engine:model or a list of them")
		}
		for _, b := range builders {
			if err := checkEngineModel("builder", b); err != nil {
				return fmt.Errorf("config: %w", err)
			}
		}
	}
	switch c.Disputes {
	case "", "ask":
	case "arbiter":
		if err := checkEngineModel("arbiter", c.Arbiter); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	default:
		return fmt.Errorf("config: disputes must be ask or arbiter")
	}
	if len(c.Quality) > 64<<10 || termSafeLines(c.Quality) != c.Quality || termSafe(c.QualitySource) != c.QualitySource {
		return fmt.Errorf("config: quality must be plain text under 64 KB")
	}
	for i, slot := range c.Reviewers {
		if len(slot) == 0 {
			return fmt.Errorf("config: slot %d has no models", i+1)
		}
		for _, model := range slot {
			if err := checkEngineModel("reviewer", model); err != nil {
				return fmt.Errorf("config: %w", err)
			}
		}
	}
	return nil
}

// checkEngineModel refuses anything but claude or codex, with or without a model (claude:opus).
func checkEngineModel(role, model string) error {
	name, m, hasModel := strings.Cut(model, ":")
	if hasModel && m == "" || strings.ContainsAny(model, ",;\n\r\t ") || termSafe(model) != model || (name != "claude" && name != "codex") {
		return fmt.Errorf("invalid %s %q; use claude, codex, claude:model or codex:model", role, model)
	}
	return nil
}

// configHash identifies a config, for the approval record.
func configHash(c *loopConfig) string {
	if c == nil {
		return ""
	}
	data, _ := json.Marshal(c)
	return briefHash(string(data))
}

// configDescription is the config as the person reads it in the approval popup.
func configDescription(c *loopConfig) string {
	var s strings.Builder
	for i, slot := range c.Reviewers {
		fmt.Fprintf(&s, "slot %d: %s\n", i+1, strings.Join(slot, " -> "))
	}
	fmt.Fprintf(&s, "failover: %t", c.Failover)
	if builders := configBuilders(c); len(builders) > 0 {
		fmt.Fprintf(&s, "\nbuilder: %s", strings.Join(builders, " -> "))
	}
	if c.QualitySource != "" {
		q := "built in"
		if c.Quality != "" {
			q = fmt.Sprintf("%s (sha256 %s)", c.QualitySource, briefHash(c.Quality)[:12])
		}
		fmt.Fprintf(&s, "\nquality standard: %s", q)
		// The text approved is the copy in the config: its first lines, so the file's state at
		// approval is what the person reads.
		if c.Quality != "" {
			lines := strings.Split(c.Quality, "\n")
			for i, l := range lines[:min(len(lines), 6)] {
				fmt.Fprintf(&s, "\n  > %s", l)
				if i == 5 && len(lines) > 6 {
					fmt.Fprintf(&s, "\n  > … %d more lines", len(lines)-6)
				}
			}
		}
	}
	switch c.Disputes {
	case "ask":
		s.WriteString("\ndisputes: ask me")
	case "arbiter":
		fmt.Fprintf(&s, "\ndisputes: arbiter %s", c.Arbiter)
	}
	return s.String()
}

// checkConfig refuses a run whose config is not the one approved with its batch.
func (r *loopRun) checkConfig() error {
	if r.Config != nil {
		if err := r.Config.validate(); err != nil {
			return err
		}
	}
	if r.Batch == "" {
		return nil
	}
	b, err := loadBatch(r.Batch)
	if err != nil {
		return err
	}
	if configHash(r.Config) != configHash(b.Config) || (len(b.Hashes) > 0 && b.ConfigHash != configHash(b.Config)) {
		return fmt.Errorf("batch config is not the one the user approved")
	}
	return nil
}
