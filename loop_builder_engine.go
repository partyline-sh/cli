package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// A builder is one headless engine run that edits the batch's checkout. Like a reviewer it runs
// with reviewerEnv's scrubbed environment (no tokens, no TMUX, no partyline session keys), so it
// cannot push, open a PR or act as the session; unlike a reviewer it may write inside the checkout
// and run commands, because it has to make the change and run the tests.

// builderSettings is the claude builder's settings on top of the person's own: nothing under
// ~/.partyline may be read or written, by the file tools (permission deny rules, which hold in
// bypassPermissions) or by Bash (its sandbox); everything else is the person's session. The
// sandbox's writes are not confined: a builder may install tools and write caches as a session can.
const builderSettings = `{"permissions":{"deny":["Read(~/.partyline/**)","Edit(~/.partyline/**)","Write(~/.partyline/**)"]},` +
	`"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false,` +
	`"filesystem":{"denyRead":["~/.partyline"],"allowWrite":["~"]},` +
	`"network":{"allowLocalBinding":true,"allowedDomains":["*"]}}}`

// builderClaudeCmd is the claude builder: the person's claude, with their CLAUDE.md files, settings,
// tools and MCP servers, approving its own tool use (nobody is there to answer a prompt), with
// builderSettings and builderEnv's two limits. stream-json, so the live view shows the run.
func builderClaudeCmd(ctx context.Context, e loopEngine, dir string) *exec.Cmd {
	args := []string{"-p", "--permission-mode", "bypassPermissions", "--settings", builderSettings,
		"--output-format", "stream-json", "--verbose", "--no-session-persistence"}
	args = append(args, e.claudeArgs()...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	cmd.Env = builderEnv(os.Environ(), builderGHConfig())
	return cmd
}

// codexBuildArgs is the codex builder's argv: the person's codex (their config, AGENTS.md files and
// rules), in the workspace-write sandbox so it writes only inside the checkout and temp (never
// partyline's state), with the network on, as a session has it.
func codexBuildArgs(e loopEngine, dir, last string) []string {
	args := []string{"exec", "-", "--json", "-s", "workspace-write", "-c", "sandbox_workspace_write.network_access=true", "-C", dir, "-o", last,
		"--skip-git-repo-check", "--ephemeral"}
	return append(args, e.codexArgs()...)
}

// runBuilder runs one builder on prompt in dir, writing a readable account of what it does to out,
// and returns its final message. A variable so tests can stand in for the engine.
var runBuilder = func(ctx context.Context, e loopEngine, dir, prompt, last string, out io.Writer) (string, error) {
	var cmd *exec.Cmd
	switch e.Name {
	case "claude":
		cmd = builderClaudeCmd(ctx, e, dir)
	case "codex":
		cmd = exec.CommandContext(ctx, "codex", codexBuildArgs(e, dir, last)...)
		cmd.Dir = dir
		cmd.Env = builderEnv(os.Environ(), builderGHConfig())
	default:
		return "", fmt.Errorf("%q cannot build; builders are claude and codex", e.Name)
	}
	// Not groupSpawn: the builder stays in the contractor runner's process group, so a hangup that
	// ends the runner's group ends the engine too.
	cmd.Stdin = strings.NewReader(prompt)
	var stderr tailBuffer // the engine's own diagnostics, matched for limits (never the model's text)
	cmd.Stderr = io.MultiWriter(out, &stderr)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	result, failure := renderBuilderStream(e.Name, stdout, out)
	err = cmd.Wait()
	if e.Name == "codex" {
		text, rerr := os.ReadFile(last)
		result = strings.TrimSpace(string(text))
		if err == nil && (rerr != nil || result == "") {
			err = fmt.Errorf("codex wrote no final message")
		}
	}
	if err == nil && failure != "" {
		err = fmt.Errorf("%s", failure)
	}
	if err != nil {
		return result, &reviewerFailure{fmt.Sprintf("%s builder: %v", e.Name, err), failure + "\n" + stderr.String()}
	}
	return result, nil
}

// tailBuffer keeps the last 64 KB written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 64<<10 {
		t.b = t.b[len(t.b)-64<<10:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }

// renderBuilderStream reads the engine's JSON events, writes each message and tool call as a line
// of text, and returns claude's final result and any error the engine reported about itself.
func renderBuilderStream(engine string, r io.Reader, out io.Writer) (result, failure string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev builderEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			fmt.Fprintln(out, termSafe(sc.Text()))
			continue
		}
		for _, line := range ev.lines(engine) {
			fmt.Fprintln(out, line)
		}
		switch {
		case ev.Type == "result":
			result = ev.Result
			if ev.IsError {
				failure = "claude reported an error: " + ev.Result // its own error, not the model's answer
			}
		case ev.Type == "error" || ev.Type == "turn.failed":
			var msg string
			_ = json.Unmarshal(ev.Message, &msg)         // a string in codex events, an object in claude's
			failure = "codex: " + msg + ev.Error.Message // whole: limits are matched on it
		}
	}
	return result, failure
}

// builderEvent holds the fields of claude's stream-json and codex's --json events that the live
// view shows.
type builderEvent struct {
	Type    string          `json:"type"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error"`
	Message json.RawMessage `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
		Changes []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
}

func (ev builderEvent) lines(engine string) []string {
	var out []string
	switch {
	case engine == "claude" && ev.Type == "assistant":
		var m struct {
			Content []struct {
				Type  string         `json:"type"`
				Text  string         `json:"text"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		}
		_ = json.Unmarshal(ev.Message, &m)
		for _, c := range m.Content {
			switch c.Type {
			case "text":
				out = append(out, termSafeLines(c.Text))
			case "tool_use":
				out = append(out, "→ "+termSafe(c.Name)+" "+clipVis(toolArg(c.Input), 160))
			}
		}
	case engine == "codex" && ev.Type == "item.completed" && ev.Item.Type == "agent_message":
		out = append(out, termSafeLines(ev.Item.Text))
	case engine == "codex" && ev.Type == "item.started" && ev.Item.Type == "command_execution":
		out = append(out, "→ "+clipVis(ev.Item.Command, 160))
	case engine == "codex" && ev.Type == "item.completed" && ev.Item.Type == "file_change":
		for _, ch := range ev.Item.Changes {
			out = append(out, "→ edit "+termSafe(ch.Path))
		}
	}
	return out
}

// toolArg is the argument that says what a tool call does: the command, the file, the pattern.
func toolArg(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "pattern", "path"} {
		if s, ok := in[k].(string); ok {
			return s
		}
	}
	return ""
}
