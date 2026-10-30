package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Token use per loop. A review keeps its tokens in its review file; builders, the arbiter and the
// decider append theirs to usage.jsonl. The card's details total both by model and role.

type loopUsage struct {
	At     time.Time  `json:"at"`
	Role   string     `json:"role"` // builder, arbiter, decider; read from review files: reviewer, brief reviewer, scope reviewer
	Round  int        `json:"round"`
	Build  int        `json:"build,omitempty"`
	Engine string     `json:"engine"`
	Tokens loopTokens `json:"tokens"`
}

// recordLoopUsage appends u to loop id's usage.jsonl. A run that reported no tokens is not
// recorded. Best effort: a lost line costs a figure in the details, never the run.
func recordLoopUsage(id string, u loopUsage) {
	if u.Tokens == (loopTokens{}) {
		return
	}
	if u.At.IsZero() {
		u.At = time.Now()
	}
	line, err := json.Marshal(u)
	if err != nil {
		return
	}
	f, err := os.OpenFile(loopFile(id, "usage.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n')) // one write: concurrent appenders do not interleave
}

// reviewFileName is a review's file: review-r2-1-claude_opus_medium.json is round 2's first
// reviewer; b is a batch's brief review, s a scope review.
var reviewFileName = regexp.MustCompile(`^review-([brs])(\d+)-\d+-.+\.json$`)

// loopUsages is every recorded run of loop id: its reviews and its usage.jsonl, oldest first.
func loopUsages(id string) []loopUsage {
	var out []loopUsage
	files, _ := filepath.Glob(loopFile(id, "review-*.json"))
	for _, file := range files {
		m := reviewFileName.FindStringSubmatch(filepath.Base(file))
		if m == nil || strings.HasSuffix(file, ".result.json") {
			continue
		}
		var rv loopReview
		readLoopJSON(id, filepath.Base(file), &rv)
		n, _ := strconv.Atoi(m[2])
		u := loopUsage{Role: map[string]string{"r": "reviewer", "b": "brief reviewer", "s": "scope reviewer"}[m[1]], Engine: rv.Engine, Tokens: rv.Tokens}
		if m[1] == "r" {
			u.Round = n
		}
		if info, err := os.Stat(file); err == nil {
			u.At = info.ModTime()
		}
		out = append(out, u)
	}
	if f, err := os.Open(loopFile(id, "usage.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var u loopUsage
			if json.Unmarshal(sc.Bytes(), &u) == nil {
				out = append(out, u)
			}
		}
		f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// usageTable is the details' token section: a line per model and role with its runs and totals,
// then the total. Fresh is input and cache writes plus output, what is paid at the full rate; cache
// reads are listed apart, since summing them makes a short run look enormous. Codex reports no cost.
func usageTable(us []loopUsage) string {
	type key struct{ engine, role string }
	type sum struct {
		runs int
		tok  loopTokens
	}
	var order []key
	sums := map[key]*sum{}
	var total loopTokens
	for _, u := range us {
		k := key{u.Engine, u.Role}
		if sums[k] == nil {
			sums[k] = &sum{}
			order = append(order, k)
		}
		sums[k].runs++
		sums[k].tok.add(u.Tokens)
		total.add(u.Tokens)
	}
	if len(order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Tokens (fresh = input + cache writes + output; cached = cache reads):\n")
	for _, k := range order {
		s := sums[k]
		fmt.Fprintf(&b, "  %s %s · %d %s · %s\n", termSafe(k.engine), k.role, s.runs, plural(s.runs, "run", "runs"), tokenFigures(s.tok))
	}
	fmt.Fprintf(&b, "  total · %s\n", tokenFigures(total))
	return b.String()
}

func tokenFigures(t loopTokens) string {
	s := fmt.Sprintf("%s fresh, %s cached", tokenCount(t.In+t.Out), tokenCount(t.Cached))
	if t.CostUSD > 0 {
		s += fmt.Sprintf(" · $%.2f", t.CostUSD)
	}
	return s
}

// tokenCount is a short count: 940, 38.4k, 2.8M.
func tokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

// contractorLastAction is the last tool call in contractor id's log ("go test ./..."), or "". Codex
// runs each command through a login shell; the command itself is what says what it is doing.
func contractorLastAction(id string) string {
	f, err := os.Open(contractorLog(id))
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 32<<10 {
		_, _ = f.Seek(-32<<10, io.SeekEnd)
	}
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if a, ok := strings.CutPrefix(sc.Text(), "→ "); ok {
			last = a
		}
	}
	return unwrapShell(last)
}

var loginShell = regexp.MustCompile(`^/bin/(?:ba|z)?sh -l?c ['"]?`)

func unwrapShell(cmd string) string {
	if loc := loginShell.FindStringIndex(cmd); loc != nil {
		cmd = strings.TrimRight(cmd[loc[1]:], `'"`)
	}
	return termSafe(strings.TrimSpace(cmd))
}
