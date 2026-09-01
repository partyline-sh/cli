package engine

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// models.go — ask the engine what it can run, instead of making a person guess.
//
// A model was a free-text field validated only by shape (`modelRe`). A typo passed validation, rode
// all the way to a worker, and failed fifteen minutes later for a reason invisible from the board.
// Several engines can answer the question themselves; the ones that can should be asked.
//
// DELIBERATELY NOT A REGISTRY. We do not ship a list of model names: it would be stale the week it
// was written, wrong for anyone on a custom endpoint, and it would claim knowledge we do not have.
// The engine on the operator's machine, configured with the operator's keys, is the only thing that
// knows what that machine can actually run.
//
// Empty means "we could not ask" — never "there are none". The caller keeps free text either way, so
// discovery is an assist, never a gate.

const modelsTimeout = 12 * time.Second

// ListModels returns the model identifiers this engine reports on THIS machine, newest listing order
// preserved where the engine implies one. nil when the engine has no listing command, is not
// installed, or errors — all of which mean the same thing to a caller: fall back to free text.
func ListModels(engine string) []string {
	spec, ok := Lookup(engine)
	if !ok || len(spec.Models) == 0 {
		return nil
	}
	if _, err := exec.LookPath(spec.Bin); err != nil {
		return nil // not installed here — not an error, just nothing to say
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, spec.Bin, spec.Models...).Output()
	if err != nil {
		return nil
	}
	return parseModels(string(out))
}

// parseModels pulls identifiers out of a listing. Kept forgiving on purpose: every CLI prints this
// differently, the format is not a contract any of them publish, and a parser that demands one shape
// breaks silently the next time a vendor pads a column.
//
// The rule is deliberately shallow — take the first token of each non-empty line, drop anything that
// reads as prose or a header. A wrong entry costs a person one glance; a parser that throws away the
// whole list costs them the feature.
func parseModels(out string) []string {
	var models []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "#") {
			continue
		}
		// Many CLIs print "Provider: model-id". Strip that label — but ONLY when the colon is part
		// of a LEADING word, or "gpt-4o (aliases: 4o)" gets cut at the wrong colon and yields "4o)".
		if i := strings.Index(line, ": "); i > 0 && i < 24 && !strings.Contains(line[:i], " ") {
			line = line[i+2:]
		}
		id := strings.Fields(line)
		if len(id) == 0 {
			continue
		}
		m := strings.Trim(id[0], ",:")
		// An identifier has no spaces and is not a sentence. Anything with a capitalised first word
		// and no punctuation is almost always a header ("Available models").
		if m == "" || len(m) > 120 || seen[m] {
			continue
		}
		seen[m] = true
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}
