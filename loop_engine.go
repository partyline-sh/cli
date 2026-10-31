package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A reviewer is one headless engine run, read-only, in a snapshot directory, with an environment
// that cannot act as the session or as the person. Only claude and codex are reviewers: their
// read-only modes are established (docs/plans/partyline-loops.md, step 0); the others' are not.

type loopEngine struct {
	Name   string // claude | codex
	Model  string // "" = the engine's default
	Effort string // "" = the engine's default; claude --effort, codex model_reasoning_effort
}

type loopTokens struct {
	In, Out, Cached int
	CostUSD         float64 // as reported by the engine; 0 when it reports none
}

func (t *loopTokens) add(o loopTokens) {
	t.In, t.Out, t.Cached, t.CostUSD = t.In+o.In, t.Out+o.Out, t.Cached+o.Cached, t.CostUSD+o.CostUSD
}

// engineUsage is the usage object of claude's result and of codex's turn.completed.
type engineUsage struct {
	In          int `json:"input_tokens"`
	Out         int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`     // claude
	CacheCreate int `json:"cache_creation_input_tokens"` // claude
	Cached      int `json:"cached_input_tokens"`         // codex
}

// tokens counts both engines alike: In is what was paid for at the full rate (input and cache
// writes), Cached the cache reads. Claude reports cache reads and writes apart from input_tokens;
// codex's cached_input_tokens are part of its input_tokens, and its output_tokens already include
// reasoning_output_tokens (51 output with 44 reasoning for a two-character answer, codex-cli 0.164.0).
func (u engineUsage) tokens() loopTokens {
	return loopTokens{In: max(0, u.In+u.CacheCreate-u.Cached), Out: u.Out, Cached: u.CacheRead + u.Cached}
}

func (e loopEngine) String() string {
	s := e.Name
	if e.Model != "" || e.Effort != "" {
		s += ":" + e.Model
	}
	if e.Effort != "" {
		s += ":" + e.Effort
	}
	return s
}

// parseLoopEngines reads "claude,codex:gpt-5,claude:opus:high".
func parseLoopEngines(spec string) ([]loopEngine, error) {
	var out []loopEngine
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rest, _ := strings.Cut(part, ":")
		model, effort, _ := strings.Cut(rest, ":")
		if name != "claude" && name != "codex" {
			return nil, fmt.Errorf("%q cannot review yet; reviewers are claude and codex", name)
		}
		if err := checkEffort(name, effort); err != nil {
			return nil, err
		}
		out = append(out, loopEngine{Name: name, Model: model, Effort: effort})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no reviewers given")
	}
	return out, nil
}

// secretEnv matches variables a reviewer must not inherit: credentials for anything other than its
// own engine, and the identifiers that let a process act as a partyline session.
var secretEnv = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|_KEY$|^AWS_|^GH_|^GITHUB_|^PARTYLINE_SESSION|^TMUX|^SSH_AUTH_SOCK$|^KUBECONFIG$|^DOCKER_HOST$|^GOOGLE_APPLICATION_CREDENTIALS$)`)

// engineAuthEnv are the prefixes an engine authenticates with; its own survive the scrub.
var engineAuthEnv = map[string][]string{
	"claude": {"ANTHROPIC_", "CLAUDE_CODE_"},
	"codex":  {"OPENAI_", "CODEX_"},
}

// reviewerEnv is the environment for one engine: everything else, minus credentials and the
// channels to other systems (SSH agent, Kubernetes, Docker), keeping that engine's own auth.
func reviewerEnv(engine string, environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		own := false
		for _, p := range engineAuthEnv[engine] {
			own = own || strings.HasPrefix(k, p)
		}
		if secretEnv.MatchString(k) && !own {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PARTYLINE=1", "PARTYLINE_MEMORY_CAPTURE=off", "PARTYLINE_MEMORY_REFRESH=off")
}

// runReviewer runs one engine on prompt in dir and returns its final text and token use.
var runReviewer = func(ctx context.Context, e loopEngine, dir, prompt string) (string, loopTokens, error) {
	switch e.Name {
	case "claude":
		return runClaudeReviewer(ctx, e, dir, prompt)
	case "codex":
		return runCodexReviewer(ctx, e, dir, prompt)
	}
	return "", loopTokens{}, fmt.Errorf("unknown engine %q", e.Name)
}

// The claude reviewer is restricted to the snapshot directory with no MCP servers (headlessClaude).
func runClaudeReviewer(ctx context.Context, e loopEngine, dir, prompt string) (string, loopTokens, error) {
	args := []string{"--output-format", "json", "--no-session-persistence"}
	args = append(args, e.claudeArgs()...)
	cmd := headlessClaude(ctx, dir, "", "Read,Glob,Grep", args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = reviewerEnv("claude", os.Environ())
	groupSpawn(cmd)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", loopTokens{}, fmt.Errorf("timed out")
	}
	var res struct {
		Result  string      `json:"result"`
		IsError bool        `json:"is_error"`
		Cost    float64     `json:"total_cost_usd"`
		Usage   engineUsage `json:"usage"`
	}
	if jerr := json.Unmarshal(out, &res); jerr != nil {
		if err != nil {
			// No result envelope: what claude printed is its own message, not a review.
			return "", loopTokens{}, &reviewerFailure{fmt.Sprintf("claude failed: %v", err), string(out) + "\n" + engineStderr(err)}
		}
		return "", loopTokens{}, fmt.Errorf("claude returned no result")
	}
	tok := res.Usage.tokens()
	tok.CostUSD = res.Cost
	if res.IsError {
		// is_error: the result is claude's error message rather than the model's answer.
		return "", tok, &reviewerFailure{fmt.Sprintf("claude reported an error: %s", clipVis(res.Result, 200)), res.Result + "\n" + engineStderr(err)}
	}
	if err != nil {
		return "", tok, &reviewerFailure{fmt.Sprintf("claude exited with an error: %v", err), engineStderr(err)}
	}
	return res.Result, tok, nil
}

func runCodexReviewer(ctx context.Context, e loopEngine, dir, prompt string) (string, loopTokens, error) {
	f, err := os.CreateTemp(filepath.Dir(dir), "codex-last-*.txt")
	if err != nil {
		return "", loopTokens{}, err
	}
	last := f.Name()
	_ = f.Close()
	// project_doc_max_bytes=0: codex otherwise follows the repository's AGENTS.md (a planted
	// instruction in it was obeyed in testing on codex-cli 0.164.0). --ignore-user-config drops
	// the MCP servers in ~/.codex/config.toml; auth still works.
	cmd := exec.CommandContext(ctx, "codex", codexReviewArgs(e, dir, last)...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = dir
	cmd.Env = reviewerEnv("codex", os.Environ())
	groupSpawn(cmd)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", loopTokens{}, fmt.Errorf("timed out")
	}
	var tok loopTokens
	var diag strings.Builder // codex's own error events, not the model's messages
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage engineUsage `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Type == "turn.completed" {
			tok.add(ev.Usage.tokens())
		}
		if ev.Type == "error" || ev.Type == "turn.failed" {
			diag.WriteString(ev.Message + ev.Error.Message + "\n")
		}
	}
	text, rerr := os.ReadFile(last)
	if err != nil || rerr != nil || strings.TrimSpace(string(text)) == "" {
		return "", tok, &reviewerFailure{fmt.Sprintf("codex failed: %v", firstErr(err, rerr)), diag.String() + engineStderr(err)}
	}
	return string(text), tok, nil
}

// codexReviewArgs is the codex argv for a review; the prompt arrives on stdin ("-"). Each flag was
// checked against codex-cli 0.164.0 (docs/plans/partyline-loops.md, step 0):
// --ephemeral keeps the run out of the session list; --skip-git-repo-check because the snapshot is
// not a repository; --ignore-user-config and --ignore-rules drop ~/.codex config and exec rules;
// project_doc_max_bytes=0 stops codex following the repository's AGENTS.md.
func codexReviewArgs(e loopEngine, dir, last string) []string {
	args := []string{"exec", "-", "--json", "-s", "read-only", "-C", dir, "-o", last,
		"--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"-c", "project_doc_max_bytes=0"}
	return append(args, e.codexArgs()...)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return fmt.Errorf("no output")
}

// reviewerFailure is a reviewer run that failed, with the engine's own diagnostics (stderr, its error
// envelope or events), which is all a usage limit is matched against: never the model's answer,
// which can quote a limit message from the code it reviews.
type reviewerFailure struct {
	msg  string
	diag string
}

func (f *reviewerFailure) Error() string { return f.msg }

// engineStderr is what a failed engine wrote to stderr.
func engineStderr(err error) string {
	if exit, ok := err.(*exec.ExitError); ok {
		return string(exit.Stderr)
	}
	return ""
}

func checkEffort(engine, effort string) error {
	if effort == "" {
		return nil
	}
	efforts := engineEffortSet(engine)
	if slices.Contains(efforts, effort) {
		return nil
	}
	return fmt.Errorf("%s effort %q: use one of %s", engine, effort, strings.Join(efforts, ", "))
}

// claudeArgs and codexArgs are the model and effort flags for e.
func (e loopEngine) claudeArgs() []string {
	var args []string
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}
	if e.Effort != "" {
		args = append(args, "--effort", e.Effort)
	}
	return args
}

func (e loopEngine) codexArgs() []string {
	var args []string
	if e.Model != "" {
		args = append(args, "-m", e.Model)
	}
	if e.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+e.Effort)
	}
	return args
}
