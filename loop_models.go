package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// The engines, models and efforts the settings offer as lists. claude's come from its CLI's own
// aliases and --effort levels; codex's from the model list codex itself caches
// (~/.codex/models_cache.json), each model with the efforts it takes. A model the person typed
// elsewhere (a --config file) still works: the lists are what is offered, not the only thing
// accepted. gemini is not a loop engine yet: it has no headless builder or reviewer runner.

type modelChoice struct {
	Name    string // what the engine's --model / -m takes
	Label   string
	Efforts []string
}

type engineChoice struct {
	Name   string
	Models []modelChoice
}

var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// codexFallbackEfforts are offered when codex has cached no model list.
var codexFallbackEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

func modelCatalog() []engineChoice {
	claude := engineChoice{Name: "claude"}
	for _, m := range []struct{ name, label string }{{"fable", "Fable (latest)"}, {"opus", "Opus (latest)"}, {"sonnet", "Sonnet (latest)"}, {"haiku", "Haiku (latest)"}} {
		claude.Models = append(claude.Models, modelChoice{Name: m.name, Label: m.label, Efforts: claudeEfforts})
	}
	return []engineChoice{claude, {Name: "codex", Models: codexModels()}}
}

// codexModels reads codex's cached list, keeping the models it lists to people.
func codexModels() []modelChoice {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
	var cache struct {
		Models []struct {
			Slug       string `json:"slug"`
			Display    string `json:"display_name"`
			Visibility string `json:"visibility"`
			Levels     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	var out []modelChoice
	if err == nil && json.Unmarshal(b, &cache) == nil {
		for _, m := range cache.Models {
			if m.Slug == "" || m.Visibility == "hide" {
				continue
			}
			c := modelChoice{Name: m.Slug, Label: m.Display}
			for _, l := range m.Levels {
				c.Efforts = append(c.Efforts, l.Effort)
			}
			if len(c.Efforts) == 0 {
				c.Efforts = codexFallbackEfforts
			}
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []modelChoice{{Name: "gpt-5", Label: "gpt-5", Efforts: codexFallbackEfforts}}
	}
	return out
}

// enginesWithEfforts is every effort an engine's models take, for checking a typed spec.
func engineEffortSet(engine string) []string {
	var out []string
	for _, e := range modelCatalog() {
		if e.Name != engine {
			continue
		}
		for _, m := range e.Models {
			for _, eff := range m.Efforts {
				if !slices.Contains(out, eff) {
					out = append(out, eff)
				}
			}
		}
	}
	if engine == "codex" {
		for _, eff := range codexFallbackEfforts {
			if !slices.Contains(out, eff) {
				out = append(out, eff)
			}
		}
	}
	return out
}
