package main

import (
	"reflect"
	"testing"
)

// skillWatch is the invocation-telemetry detector: given the injected skill set, it reads the
// stream-json event lines and reports which skills the agent actually USED. These tests pin the two
// detection signals (claude's Skill tool + a touch of the skill's materialized dir) AND the guards
// that keep it honest — matching is scoped to the injected set, and only assistant tool_use blocks
// are inspected, so the injected manifest (which rides in the prompt) can never self-trigger.
func TestSkillWatchInspect(t *testing.T) {
	// The injected set for every case below.
	names := []string{"deploy-helper", "db-migrate"}

	// A stream line helpers keep the cases readable.
	assistantToolUse := func(name, inputJSON string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","input":` + inputJSON + `}]}}`
	}

	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{
			name:  "claude Skill tool names the skill (command key)",
			lines: []string{assistantToolUse("Skill", `{"command":"deploy-helper"}`)},
			want:  []string{"deploy-helper"},
		},
		{
			name:  "claude Skill tool names the skill (name key)",
			lines: []string{assistantToolUse("Skill", `{"name":"db-migrate"}`)},
			want:  []string{"db-migrate"},
		},
		{
			name:  "Read of the skill's SKILL.md (.claude symlink)",
			lines: []string{assistantToolUse("Read", `{"file_path":"/wt/.claude/skills/deploy-helper/SKILL.md"}`)},
			want:  []string{"deploy-helper"},
		},
		{
			name:  "Bash running a bundled script under .agents/skills",
			lines: []string{assistantToolUse("Bash", `{"command":"bash .agents/skills/db-migrate/scripts/run.sh --yes"}`)},
			want:  []string{"db-migrate"},
		},
		{
			name: "both skills across multiple events, deduped + sorted",
			lines: []string{
				assistantToolUse("Read", `{"file_path":".claude/skills/deploy-helper/SKILL.md"}`),
				assistantToolUse("Read", `{"file_path":".claude/skills/deploy-helper/references/notes.md"}`),
				assistantToolUse("Skill", `{"command":"db-migrate"}`),
			},
			want: []string{"db-migrate", "deploy-helper"},
		},
		{
			name:  "case-insensitive path + name match",
			lines: []string{assistantToolUse("Read", `{"file_path":"/WT/.Claude/Skills/Deploy-Helper/SKILL.md"}`)},
			want:  []string{"deploy-helper"},
		},
		{
			name:  "unrelated edit is not a use",
			lines: []string{assistantToolUse("Edit", `{"file_path":"src/main.go"}`)},
			want:  nil,
		},
		{
			name:  "a path for a NON-injected skill name is ignored (scoped to the set)",
			lines: []string{assistantToolUse("Read", `{"file_path":".claude/skills/some-other-skill/SKILL.md"}`)},
			want:  nil,
		},
		{
			name:  "the skill NAME merely mentioned in prose (text block) is not a use",
			lines: []string{`{"type":"assistant","message":{"content":[{"type":"text","text":"I could use deploy-helper here but won't."}]}}`},
			want:  nil,
		},
		{
			name:  "a user/prompt message carrying the injected manifest never self-triggers",
			lines: []string{`{"type":"user","message":{"content":[{"type":"text","text":"Installed skills: deploy-helper — deploys; db-migrate — migrates."}]}}`},
			want:  nil,
		},
		{
			name:  "the bare skill name inside a normal file path is not a Skill-tool activation",
			lines: []string{assistantToolUse("Read", `{"file_path":"docs/deploy-helper.md"}`)},
			want:  nil,
		},
		{
			name:  "result/system frames are ignored",
			lines: []string{`{"type":"system","subtype":"init","session_id":"x"}`, `{"type":"result","result":"done"}`},
			want:  nil,
		},
		{
			name:  "malformed line is skipped, not fatal",
			lines: []string{`{not json`, assistantToolUse("Skill", `{"command":"deploy-helper"}`)},
			want:  []string{"deploy-helper"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newSkillWatch(names)
			for _, ln := range tc.lines {
				w.inspect([]byte(ln))
			}
			got := w.invoked()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("invoked() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An empty injected set (ptln work / describe, or a run with no org skills) must do nothing and never
// allocate a false positive.
func TestSkillWatchEmptySet(t *testing.T) {
	w := newSkillWatch(nil)
	w.inspect([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"command":"anything"}}]}}`))
	if got := w.invoked(); got != nil {
		t.Errorf("empty-set watcher invoked() = %v, want nil", got)
	}
}
