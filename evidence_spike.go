package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// evidence_spike.go — a standalone harness for the EVIDENCE GATE, the make-or-break of
// grounded parties. It proves (or disproves) the core behavior before we wire it into the
// party runner: an expert researches a real repo, may only answer inside cited `position`
// blocks, we RE-FETCH each cited source ourselves (don't trust the agent's quote), a cheap
// second model verifies the claim against that real content, and anything un-cited or
// unsupported is DROPPED — not posted.
//
//	cd <repo> && ptln evidence-spike "Is the party token ever sent on a command line?"
//
// Hidden/dev command. Needs `claude` on PATH. The point is to look at the output and decide
// whether the gate yields sharp, cited, honest findings — or mush.
func evidenceSpikeMain(args []string) {
	fs := flag.NewFlagSet("evidence-spike", flag.ExitOnError)
	model := fs.String("model", "", "expert model (default: claude's default)")
	verifier := fs.String("verifier", "haiku", "independent verifier model (cheap + separate on purpose)")
	noVerify := fs.Bool("no-verify", false, "skip the verification pass (citation-gate only)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fatal(fmt.Errorf(`usage: ptln evidence-spike "<question about this repo>"`))
	}
	question := strings.Join(fs.Args(), " ")
	dir, _ := os.Getwd()

	fmt.Printf("⏳ %s is researching %s …\n\n", bold("expert"), filepath.Base(dir))
	raw, err := runClaude(dir, expertPrompt(question), *model, []string{"Read", "Grep", "Glob"})
	if err != nil {
		fatal(fmt.Errorf("expert run failed: %w", err))
	}

	positions, droppedProse := parsePositions(raw)
	if len(positions) == 0 {
		fmt.Println("— the expert produced no cited position. Honest silence beats a guess. —")
		if strings.TrimSpace(droppedProse) != "" {
			fmt.Printf("\n(dropped un-cited prose:\n%s\n)\n", indent(droppedProse))
		}
		return
	}

	var kept, dropped int
	for _, p := range positions {
		// citation gate: a position with no citations never survives.
		if len(p.cites) == 0 {
			fmt.Printf("✗ DROPPED (no citation): %s\n\n", p.claim)
			dropped++
			continue
		}
		// re-fetch + verify each cite against the REAL content.
		supported := false
		var evidence []string
		for _, c := range p.cites {
			content := refetch(dir, c.locator)
			ok := true
			why := "citation-gate only (verification skipped)"
			if !*noVerify {
				ok, why = verifyClaim(dir, p.claim, c.locator, content, *verifier)
			}
			mark := "✓"
			if !ok {
				mark = "·"
			}
			supported = supported || ok
			evidence = append(evidence, fmt.Sprintf("    %s %s — %s", mark, c.locator, evClip(why, 100)))
		}
		if supported {
			fmt.Printf("%s %s\n%s\n\n", green("✓ VERIFIED"), bold(p.claim), strings.Join(evidence, "\n"))
			kept++
		} else {
			fmt.Printf("✗ DROPPED (no cite held up): %s\n%s\n\n", p.claim, strings.Join(evidence, "\n"))
			dropped++
		}
	}
	fmt.Printf("— %d verified · %d dropped —\n", kept, dropped)
	if strings.TrimSpace(droppedProse) != "" {
		fmt.Printf("(also dropped un-cited prose the expert wrote outside position blocks)\n")
	}
}

func expertPrompt(question string) string {
	return "You are a grounded expert. RESEARCH this repository with your Read/Grep/Glob tools to answer the " +
		"question below, then answer ONLY inside one or more position blocks in EXACTLY this format:\n\n" +
		"```position\nclaim: <one specific, falsifiable sentence>\ncite: <file:line> — <short quote or note>\n```\n\n" +
		"Rules: every claim needs at least one `cite:` pointing at a real file:line you actually read. If you " +
		"cannot cite evidence, output NOTHING. Do not write any prose outside position blocks. Multiple distinct " +
		"findings → multiple blocks.\n\nQuestion: " + question
}

type citation struct{ locator, note string }
type position struct {
	claim string
	cites []citation
}

var posBlockRe = regexp.MustCompile("(?s)```position\\s*(.*?)```")

// parsePositions pulls cited position blocks out of the agent's output; everything outside
// a block is "dropped prose" (the gate's whole point — un-cited text never reaches anyone).
func parsePositions(out string) ([]position, string) {
	var positions []position
	prose := posBlockRe.ReplaceAllString(out, "")
	for _, m := range posBlockRe.FindAllStringSubmatch(out, -1) {
		var p position
		for _, line := range strings.Split(m[1], "\n") {
			line = strings.TrimSpace(line)
			if rest, ok := cut(line, "claim:"); ok {
				p.claim = strings.TrimSpace(rest)
			} else if rest, ok := cut(line, "cite:"); ok {
				loc, note, _ := strings.Cut(strings.TrimSpace(rest), " — ")
				p.cites = append(p.cites, citation{locator: strings.TrimSpace(loc), note: strings.TrimSpace(note)})
			}
		}
		if p.claim != "" {
			positions = append(positions, p)
		}
	}
	return positions, strings.TrimSpace(prose)
}

// refetch reads the cited source OURSELVES — the verifier sees ground truth, not the agent's
// quote. file:line → a window around the line; bare path → the head; anything else → "".
func refetch(dir, locator string) string {
	path, lineStr, hasLine := strings.Cut(locator, ":")
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSpace(path)))
	if err != nil {
		return "" // not a local file (URL/ticket) → unverifiable here
	}
	lines := strings.Split(string(data), "\n")
	if hasLine {
		if ln := firstInt(lineStr); ln > 0 {
			lo, hi := max(0, ln-6), min(len(lines), ln+5)
			return strings.Join(lines[lo:hi], "\n")
		}
	}
	if len(lines) > 60 {
		lines = lines[:60]
	}
	return strings.Join(lines, "\n")
}

// verifyClaim asks an INDEPENDENT (cheap) model whether the re-fetched source supports the
// claim. Defaults to NO when it can't re-fetch or the verifier is unsure. Claude-only today:
// the evidence gate's verifier is deliberately tool-less-ish and cheap, and its callers (the
// party runner, the spike CLI) hand it a claude model token; when a party runs a non-claude
// engine the gate still verifies with local claude — verifier ≠ producer, which is if anything
// stronger. Revisit if a machine ever runs parties without claude installed.
func verifyClaim(dir, claim, locator, content, model string) (bool, string) {
	if strings.TrimSpace(content) == "" {
		return false, "could not re-fetch this source locally (not a file path?)"
	}
	prompt := fmt.Sprintf("Verify a claim against its cited source. Be strict: answer YES only if the source "+
		"genuinely supports the claim.\n\nCited source (%s), re-fetched verbatim:\n---\n%s\n---\n\nClaim: %q\n\n"+
		"Reply with YES or NO on the first line, then one sentence why.", locator, content, claim)
	out, err := runClaude(dir, prompt, model, nil)
	if err != nil {
		return false, "verifier error: " + err.Error()
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(first)), "YES"), strings.TrimSpace(out)
}

// runClaude runs `claude -p` headless in dir (so Read/Grep operate on the repo) and returns
// its text output. allowedTools pre-authorizes read tools so a headless run doesn't prompt.
func runClaude(dir, prompt, model string, allowedTools []string) (string, error) {
	cargs := []string{"-p", prompt}
	if model != "" {
		cargs = append(cargs, "--model", model)
	}
	if len(allowedTools) > 0 {
		cargs = append(cargs, "--allowedTools", strings.Join(allowedTools, ","))
	}
	cmd := exec.Command("claude", cargs...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// --- small helpers ---
func cut(line, prefix string) (string, bool) {
	if strings.HasPrefix(strings.ToLower(line), prefix) {
		return line[len(prefix):], true
	}
	return "", false
}
func firstInt(s string) int {
	m := regexp.MustCompile(`\d+`).FindString(s)
	n, _ := strconv.Atoi(m)
	return n
}
func evClip(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }
func bold(s string) string   { return "\x1b[1m" + s + "\x1b[0m" }
func green(s string) string  { return "\x1b[32m" + s + "\x1b[0m" }
