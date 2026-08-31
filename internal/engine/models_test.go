package engine

import "testing"

// Every CLI prints a listing differently and none of them publish the format as a contract. A parser
// that demands one shape breaks silently the next time a vendor pads a column, so this is forgiving
// by design — a wrong entry costs a glance, a strict parser costs the whole feature.
func TestModelListingsAreParsedForgivingly(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      []string
	}{
		{"plain ids", "gpt-4o\nclaude-sonnet-4\n", []string{"claude-sonnet-4", "gpt-4o"}},
		{"provider prefixed", "OpenAI: gpt-4o\nAnthropic: claude-opus-4\n", []string{"claude-opus-4", "gpt-4o"}},
		{"trailing aliases", "gpt-4o (aliases: 4o)\nllama3.2 (local)\n", []string{"gpt-4o", "llama3.2"}},
		{"headers and rules dropped", "# Available\n---\ngpt-4o\n", []string{"gpt-4o"}},
		{"blank lines", "\n\ngpt-4o\n\n", []string{"gpt-4o"}},
		{"duplicates collapse", "gpt-4o\ngpt-4o\n", []string{"gpt-4o"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseModels(tc.out)
			if len(got) != len(tc.want) {
				t.Fatalf("parseModels(%q) = %v, want %v", tc.out, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseModels(%q)[%d] = %q, want %q", tc.out, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Empty means "we could not ask", never "there are none" — the caller keeps free text either way, so
// discovery assists and never gates.
func TestAnEngineThatCannotListSaysNothing(t *testing.T) {
	if got := ListModels("claude"); got != nil {
		t.Errorf("claude has no listing command but returned %v", got)
	}
	if got := ListModels("not-an-engine"); got != nil {
		t.Errorf("an unknown engine returned %v", got)
	}
}

// The bridge engine is the one that reaches a custom endpoint, so it is the one that must be able to
// report what that endpoint serves.
func TestTheBridgeEngineCanBeAsked(t *testing.T) {
	spec, ok := Lookup("llm")
	if !ok {
		t.Fatal("the llm bridge is not registered — custom endpoints have no route")
	}
	if len(spec.Models) == 0 {
		t.Error("the bridge cannot be asked what it serves, which is the point of having it")
	}
	if spec.Caps.Resume || spec.Caps.Stream || spec.Caps.MCPPerInvocation {
		t.Error("the bridge claims an agent capability it does not have; something will assume a loop it cannot run")
	}
}

// A new engine must appear in the canonical list, or nothing can select it.
func TestTheBridgeIsSelectable(t *testing.T) {
	for _, n := range Names() {
		if n == "llm" {
			return
		}
	}
	t.Error("llm is registered but not in Names() — it cannot be chosen")
}

// prime-agent is registered because it is a CLI like every other engine here — and because
// partyline supplies the thing its own docs say it lacks: every task already runs in an isolated
// git worktree, where prime-agent run directly has no sandbox at all.
func TestPrimeAgentIsSelectableAndClaimsNothing(t *testing.T) {
	spec, ok := Lookup("prime-agent")
	if !ok {
		t.Fatal("prime-agent is not registered")
	}
	if spec.Bin != "prime-agent" {
		t.Errorf("binary is %q", spec.Bin)
	}
	// Unverified is not absent. A wrong capability fails silently fifteen minutes into a run;
	// an absent one costs a feature nobody was relying on yet.
	if spec.Caps.Resume || spec.Caps.Stream || spec.Caps.Vision || spec.Caps.MCPPerInvocation {
		t.Error("prime-agent claims a capability nobody verified")
	}
	// -p is the one-shot print mode; the prompt is positional, so passthrough must come before it.
	args := spec.Args("", nil)
	if len(args) == 0 || args[0] != "-p" {
		t.Errorf("args = %v, want the one-shot print flag first", args)
	}
	// crank owns the unattended loop and the verify gate. Passing prime-agent's own would
	// double-charge and could disagree with ours about the same work.
	for _, a := range spec.Args("some-model", []string{"--extra"}) {
		if a == "--autonomous" || a == "--autonomous-gate" {
			t.Errorf("args include %q — that is prime-agent's own loop competing with crank's", a)
		}
	}
	if got := spec.Args("gpt-5", nil); len(got) < 3 || got[1] != "--model" || got[2] != "gpt-5" {
		t.Errorf("model selection = %v, want --model <pattern>", got)
	}
	for _, n := range Names() {
		if n == "prime-agent" {
			return
		}
	}
	t.Error("prime-agent is registered but not in Names() — it cannot be chosen")
}
