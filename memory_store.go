package main

// PROJECT MEMORY: what the team has learned, as files.
//
// The unit is one fact per file — a decision, a constraint, a contract, a gotcha — under
// `memory/` in the project's memory repo. One file per fact is not a filing preference: it is
// what makes two people's agents able to write at the same time without ever conflicting on the
// same line. Git merges two new files trivially; it cannot merge two rewrites of one list.
//
// Facts are markdown with front matter so a human can read, edit and review them in a pull
// request. Nothing here is a database.
//
// Supersession is explicit: a newer fact names the id it replaces, and the replaced one stops
// being briefed while staying in history. Without that a memory becomes a pile of contradictions
// that nobody trusts, which is how the previous design died.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// factKinds are the only kinds. A closed set keeps the brief scannable and stops "memory" from
// becoming a diary: chatter has no kind and therefore no home.
var factKinds = []string{"decision", "constraint", "contract", "gotcha", "question"}

type fact struct {
	ID         string
	Kind       string
	Repo       string // the member repo this is about ("" = the whole project)
	By         string
	At         time.Time
	Supersedes string
	Tags       []string
	Body       string
	Path       string
}

func memoryDir(workspaceDir string) string { return filepath.Join(workspaceDir, "memory") }

// newFactID is sortable by time and collision-free without coordination: two agents on two
// machines mint ids at the same second without a server to arbitrate.
func newFactID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// writeFact stores one fact and returns its path.
func writeFact(workspaceDir string, f fact) (string, error) {
	if !validFactKind(f.Kind) {
		return "", fmt.Errorf("kind must be one of %s", strings.Join(factKinds, ", "))
	}
	if strings.TrimSpace(f.Body) == "" {
		return "", fmt.Errorf("a fact needs a body")
	}
	if f.ID == "" {
		f.ID = newFactID(f.At)
	}
	dir := memoryDir(workspaceDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", f.ID)
	fmt.Fprintf(&b, "kind: %s\n", f.Kind)
	if f.Repo != "" {
		fmt.Fprintf(&b, "repo: %s\n", f.Repo)
	}
	fmt.Fprintf(&b, "by: %s\n", f.By)
	fmt.Fprintf(&b, "at: %s\n", f.At.UTC().Format(time.RFC3339))
	if f.Supersedes != "" {
		fmt.Fprintf(&b, "supersedes: %s\n", f.Supersedes)
	}
	if len(f.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(f.Tags, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(f.Body) + "\n")

	path := filepath.Join(dir, f.ID+".md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func validFactKind(k string) bool {
	for _, v := range factKinds {
		if v == k {
			return true
		}
	}
	return false
}

// readFacts loads every fact, newest first, with superseded ones dropped unless all is set.
func readFacts(workspaceDir string, all bool) ([]fact, error) {
	dir := memoryDir(workspaceDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // a project with nothing learned yet is not an error
		}
		return nil, err
	}
	var out []fact
	retired := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // an unreadable file must not take the whole brief down
		}
		f, ok := parseFact(string(b))
		if !ok {
			continue
		}
		f.Path = filepath.Join(dir, e.Name())
		if f.Supersedes != "" {
			retired[f.Supersedes] = true
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if all {
		return out, nil
	}
	kept := out[:0]
	for _, f := range out {
		if !retired[f.ID] {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// parseFact reads the front matter. Tolerant by design: a fact a human hand-edited slightly
// wrong should still be briefed, not silently dropped.
func parseFact(s string) (fact, bool) {
	if !strings.HasPrefix(s, "---") {
		return fact{}, false
	}
	rest := strings.TrimPrefix(s, "---")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fact{}, false
	}
	head, body := rest[:end], rest[end+4:]
	f := fact{Body: strings.TrimSpace(body)}
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "id":
			f.ID = v
		case "kind":
			f.Kind = v
		case "repo":
			f.Repo = v
		case "by":
			f.By = v
		case "supersedes":
			f.Supersedes = v
		case "tags":
			for _, t := range strings.Split(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					f.Tags = append(f.Tags, t)
				}
			}
		case "at":
			if ts, err := time.Parse(time.RFC3339, v); err == nil {
				f.At = ts
			}
		}
	}
	return f, f.ID != "" && f.Kind != ""
}
