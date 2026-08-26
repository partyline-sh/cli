package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

// The regenerate half of `ptln review`: the human's marks go to the model, the model rewrites the
// page, and the viewer swaps to the new version without anyone leaving the loop.
//
// Inference runs on the USER'S OWN engine, locally — partyline holds no API key and never has. That
// is the same rule the describe flow follows, and it is why this is a local command rather than a
// server job.

const regenTimeout = 6 * time.Minute

// regeneratePrompt turns marks into an instruction. Two rules carry the weight:
//
// It must return a COMPLETE document, because a diff or a fragment cannot be swapped into the frame,
// and a model asked for "the changed part" reliably returns something that does not stand alone.
//
// It must NOT answer `question` marks. A question is, by definition, the thing only the human can
// settle — a model that quietly picks an answer produces a mockup that looks agreed and isn't, which
// is the exact failure the typed vocabulary exists to prevent.
func regeneratePrompt(html string, marks []api.Annotation) string {
	var reqs, questions []api.Annotation
	for _, m := range marks {
		if m.Kind == "question" {
			questions = append(questions, m)
		} else {
			reqs = append(reqs, m)
		}
	}

	var b strings.Builder
	b.WriteString("You are revising a low-fidelity HTML mockup during planning. A human has marked it up.\n\n")
	b.WriteString("Return the COMPLETE revised HTML document and NOTHING else — no commentary, no diff, no\n")
	b.WriteString("explanation. It is loaded directly into a frame, so a fragment is unusable. Keep it\n")
	b.WriteString("self-contained: inline all CSS, no external scripts, stylesheets or fonts (they will not load).\n")
	b.WriteString("Stay LOW FIDELITY — real structure and real copy, not design polish. Change what the marks\n")
	b.WriteString("ask for and leave everything else alone.\n")

	if len(reqs) > 0 {
		b.WriteString("\nSATISFY THESE:\n")
		for i, m := range reqs {
			fmt.Fprintf(&b, "\n%d. [%s] %s\n", i+1, m.Kind, m.Body)
			if m.Anchor.Selector != "" {
				fmt.Fprintf(&b, "   element: %s\n", m.Anchor.Selector)
			}
			if m.Anchor.Text != "" {
				fmt.Fprintf(&b, "   its text: %q\n", m.Anchor.Text)
			}
			if m.Anchor.Viewport != "" && m.Anchor.Viewport != "desktop" {
				fmt.Fprintf(&b, "   applies at the %s width — do not break the wider layout to satisfy it\n", m.Anchor.Viewport)
			}
		}
	}

	if len(questions) > 0 {
		b.WriteString("\nDO NOT ANSWER THESE. They are open questions only the human can settle.\n")
		b.WriteString("Leave the parts they refer to EXACTLY as they are — do not guess, do not pick a default,\n")
		b.WriteString("do not remove the ambiguity. They are listed so you avoid accidentally resolving them:\n")
		for i, m := range questions {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, m.Body)
			if m.Anchor.Selector != "" {
				fmt.Fprintf(&b, "   element: %s\n", m.Anchor.Selector)
			}
		}
	}

	b.WriteString("\nHere is the current document:\n\n")
	b.WriteString(html)
	return b.String()
}

var (
	fencedHTML = regexp.MustCompile("(?s)```(?:html)?\\s*(<!.*?)```")
	bareHTML   = regexp.MustCompile(`(?is)<!doctype html.*</html>|<html[\s>].*</html>`)
)

// extractDocument pulls a complete HTML document out of whatever the model returned.
//
// It fails rather than guessing. A partial document swapped into the frame renders as a broken page
// that looks like the regeneration "worked", and the human then marks up garbage — far worse than
// being told the turn produced nothing usable and being handed the previous version back.
func extractDocument(out string) (string, error) {
	if m := fencedHTML.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1]), nil
	}
	if m := bareHTML.FindString(out); m != "" {
		return strings.TrimSpace(m), nil
	}
	return "", fmt.Errorf("the model did not return a complete HTML document")
}

// regenerate runs one revision turn on the user's own engine, streaming progress to sink.
func regenerate(ctx context.Context, dir, model, html string, marks []api.Annotation, sink func(string)) (string, error) {
	prompt := regeneratePrompt(html, marks)

	spec, ok := eng.Lookup("claude")
	if !ok {
		return "", fmt.Errorf("claude is not a known engine")
	}
	_ = spec

	ctx, cancel := context.WithTimeout(ctx, regenTimeout)
	defer cancel()

	// stream-json + --verbose is what gives us per-step events to show; ToolsReadOnly lets the model
	// consult the repo for real component names and copy, but never write to it — a mockup revision
	// has no business editing the working tree.
	cargs := []string{"-p", prompt, "--output-format", "stream-json", "--verbose",
		"--allowedTools", "Read Grep Glob",
		"--disallowedTools", "Bash,Edit,Write,MultiEdit,NotebookEdit,WebFetch,WebSearch,Task",
		"--strict-mcp-config"}
	if model != "" {
		cargs = append(cargs, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "claude", cargs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PARTYLINE=1")
	groupSpawn(cmd) // killable as a tree

	outcome, err := runWorkerStreaming(ctx, cmd, regenTimeout, sink)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("the revision turn timed out after %s", regenTimeout)
		}
		return "", fmt.Errorf("the engine turn failed: %w", err)
	}
	return extractDocument(outcome.text)
}
