package main

// `ptln memory build` — seed a repo that has no facts, and rebuild one whose facts have rotted.
//
// Seeding and rebuilding are deliberately the SAME operation. Two code paths would diverge, and
// the second one — the one that runs on a repo people have been using — is the one that matters.
//
// THE RULE THIS WHOLE FILE IS BUILT AROUND: a fact needs evidence; a guess becomes a question.
//
// A seed full of confident, subtly wrong architecture claims is worse than no facts at all,
// because it poisons every session afterwards and nobody goes back to re-read it. So nothing here
// infers intent. Every derived fact cites the file it came from in `Sources`, and everything that
// would require knowing WHY — why this exists, who depends on it, what must never change — is
// recorded as an open `question`. A later session then reads "nobody has established whether X"
// instead of inventing an answer, which is the failure the whole system exists to prevent.
//
// WHAT IT WILL NOT DO: touch a fact a human wrote. Derived facts carry a `derived` tag and are the
// only ones build will rewrite or remove. Anything you recorded yourself is invisible to it.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// derivedTag marks a fact as machine-derived and therefore safe for build to replace. Its absence
// is what protects everything a human wrote.
const derivedTag = "derived"

// topicTag namespaces a derived fact so a rebuild can find the previous version of the SAME claim
// and rewrite it, rather than appending a second copy beside it. Append-only is what broke the old
// store; this is the mechanism that stops build from recreating that failure at scale.
const topicPrefix = "topic:"

// derivedFact is one claim build is prepared to stand behind, with its citation.
type derivedFact struct {
	Topic   string
	Kind    string
	Body    string
	Sources []string
	Extra   []string // additional tags, e.g. overview/architecture for map placement
}

func (d derivedFact) tags() []string {
	t := []string{derivedTag, topicPrefix + d.Topic}
	return append(t, d.Extra...)
}

// skipDirs are never walked. Reading them wastes the budget and produces facts about other
// people's code.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".next": true, "target": true, ".venv": true, "venv": true, "__pycache__": true,
	".terraform": true, "coverage": true,
}

// deriveFacts reads the repo and returns only what it can cite.
//
// The reading ORDER is the point: build files, entry points, CI and migrations first, because
// those state intent explicitly and cheaply. Reading uniformly would spend the whole budget on
// generated code.
func deriveFacts(root string) []derivedFact {
	var out []derivedFact
	add := func(d derivedFact) {
		if strings.TrimSpace(d.Body) != "" {
			out = append(out, d)
		}
	}

	// Layout, entry points and churn used to be recorded here as separate facts. They read as
	// fragments with no context, and the generated overview (memory_overview.go) describes the
	// layout and components properly, so only facts that are rules in their own right remain.
	inv := scanRepo(root)
	if d, ok := deriveCommands(root, inv); ok {
		add(d)
	}
	if d, ok := deriveCI(root); ok {
		add(d)
	}
	if d, ok := deriveMigrations(inv); ok {
		add(d)
	}
	return out
}

// repoInventory is what one walk of the tree establishes.
type repoInventory struct {
	root       string
	byExt      map[string]int
	topDirs    []string
	files      map[string]bool // repo-relative paths of notable files
	entry      []string
	migrations []string
}

func scanRepo(root string) repoInventory {
	inv := repoInventory{root: root, byExt: map[string]int{}, files: map[string]bool{}}
	topSeen := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if de.IsDir() {
			if rel == "." {
				return nil
			}
			if skipDirs[de.Name()] || strings.HasPrefix(de.Name(), ".") && de.Name() != ".github" {
				return filepath.SkipDir
			}
			if !strings.Contains(rel, string(filepath.Separator)) && !topSeen[rel] {
				topSeen[rel] = true
				inv.topDirs = append(inv.topDirs, rel)
			}
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(rel)); ext != "" {
			inv.byExt[ext]++
		}
		inv.files[rel] = true
		base := strings.ToLower(filepath.Base(rel))
		if base == "main.go" || base == "main.py" || base == "index.ts" || base == "index.js" || base == "main.rs" {
			inv.entry = append(inv.entry, rel)
		}
		if strings.Contains(strings.ToLower(rel), "migration") && strings.HasSuffix(base, ".sql") {
			inv.migrations = append(inv.migrations, rel)
		}
		return nil
	})
	sort.Strings(inv.topDirs)
	sort.Strings(inv.entry)
	sort.Strings(inv.migrations)
	return inv
}

var extLang = map[string]string{
	".go": "Go", ".ts": "TypeScript", ".tsx": "TypeScript", ".js": "JavaScript",
	".jsx": "JavaScript", ".py": "Python", ".rs": "Rust", ".java": "Java",
	".rb": "Ruby", ".c": "C", ".cc": "C++", ".cpp": "C++", ".swift": "Swift",
	".kt": "Kotlin", ".php": "PHP", ".sql": "SQL", ".sh": "Shell",
}

// deriveCommands reads the build and test commands out of the files that declare them. These are
// the single most useful derived facts, because getting them wrong wastes a session's first ten
// minutes every time.
func deriveCommands(root string, inv repoInventory) (derivedFact, bool) {
	var lines, srcs []string
	if inv.files["Makefile"] {
		if targets := makeTargets(filepath.Join(root, "Makefile")); len(targets) > 0 {
			lines = append(lines, "make targets: "+strings.Join(targets, ", "))
			srcs = append(srcs, "file:Makefile")
		}
	}
	if inv.files["package.json"] {
		if scripts := jsonKeys(filepath.Join(root, "package.json"), "scripts"); len(scripts) > 0 {
			lines = append(lines, "npm scripts: "+strings.Join(scripts, ", "))
			srcs = append(srcs, "file:package.json")
		}
	}
	if inv.files["go.mod"] {
		lines = append(lines, "a Go module, so `go build ./...` and `go test ./...` apply")
		srcs = append(srcs, "file:go.mod")
	}
	if len(lines) == 0 {
		return derivedFact{}, false
	}
	return derivedFact{
		Topic: "commands", Kind: "contract",
		Body:    "Build and test: " + strings.Join(lines, "; ") + ".",
		Sources: srcs,
	}, true
}

func deriveCI(root string) (derivedFact, bool) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return derivedFact{}, false
	}
	var names, srcs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".yml" && ext != ".yaml" {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ext))
		srcs = append(srcs, "file:.github/workflows/"+e.Name())
	}
	if len(names) == 0 {
		return derivedFact{}, false
	}
	sort.Strings(names)
	return derivedFact{
		Topic: "ci", Kind: "contract",
		Body:    fmt.Sprintf("CI is GitHub Actions (%s). A change must pass it before merging.", strings.Join(names, ", ")),
		Sources: srcs,
	}, true
}

func deriveMigrations(inv repoInventory) (derivedFact, bool) {
	if len(inv.migrations) == 0 {
		return derivedFact{}, false
	}
	dirs := map[string]bool{}
	for _, m := range inv.migrations {
		dirs[filepath.Dir(m)] = true
	}
	var ds []string
	for d := range dirs {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	return derivedFact{
		Topic: "migrations", Kind: "constraint",
		Body: fmt.Sprintf("The database schema is defined by %d SQL migration(s) under %s. Change the schema by adding a migration; do not edit existing ones.",
			len(inv.migrations), strings.Join(ds, ", ")),
		Sources: []string{"scan:migrations"},
	}, true
}

// buildQuestions are the things reading the code cannot answer. Most are about purpose and people,
// not code: who it is for, what it replaced, who to ask, what breaks without it. A first real run
// asked only code archaeology, and the answers that made the memory worth having were exactly the
// ones no question asked for. Kept short: forty questions get skipped, a handful get answered.
//
// They are recorded as `question` facts rather than asked interactively, because the person who
// can answer them is usually not the person running the command. In the setup a session runs, the
// agent drafts answers from the README and docs and asks the person to correct them.
var buildQuestions = []derivedFact{
	{Topic: "q-purpose", Kind: "question", Extra: []string{"overview"},
		Body:    "Who is this for, and what problem does it solve for them?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-before", Kind: "question",
		Body:    "What did people do before this existed, and what did it replace?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-owner", Kind: "question",
		Body:    "Who maintains this, and who should be asked when it breaks?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-impact", Kind: "question",
		Body:    "What stops working for people or the business if this goes down?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-next", Kind: "question",
		Body:    "What should be built on this next, and what has been ruled out?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-never-change", Kind: "question",
		Body:    "What must not change because another system depends on it (wire formats, file names, orderings)?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-deploy", Kind: "question",
		Body:    "How is this deployed, and what must be deployed with it or before it?",
		Sources: []string{"build:not-derivable-from-code"}},
	{Topic: "q-deliberately-weird", Kind: "question",
		Body:    "What looks wrong in this code but is intentional?",
		Sources: []string{"build:not-derivable-from-code"}},
}

// buildAction is one change build proposes.
type buildAction struct {
	Verb  string // "add" | "update" | "remove"
	Topic string
	Kind  string
	Body  string
	Prev  *fact // the existing derived fact, for update/remove
	Src   []string
	Tags  []string
}

// planBuild diffs what the repo says now against the derived facts already recorded.
//
// It returns REMOVALS as proposals, never as silent deletions: a derived fact that no longer
// matches the repo may be stale, or may be the repo having temporarily lost a file. Showing the
// removal and letting a human confirm is the difference between a rebuild and data loss.
func planBuild(root string) ([]buildAction, error) {
	existing, err := readFacts(root, false)
	if err != nil {
		return nil, err
	}
	// Index ONLY derived facts. Anything a human wrote has no `derived` tag and is therefore
	// invisible here — build cannot rewrite or remove it, by construction.
	prev := map[string]fact{}
	for _, f := range existing {
		if !hasTag(f, derivedTag) {
			continue
		}
		if topic := topicOf(f); topic != "" {
			prev[topic] = f
		}
	}

	// The open questions are seeded once, on first setup. After that, a question that is gone was
	// answered or rejected by someone, and proposing it again on every rebuild would undo that.
	want := deriveFacts(root)
	if len(existing) == 0 {
		want = append(want, buildQuestions...)
	}
	var actions []buildAction
	seen := map[string]bool{}
	for _, d := range want {
		seen[d.Topic] = true
		if old, ok := prev[d.Topic]; ok {
			if strings.TrimSpace(old.Body) == strings.TrimSpace(d.Body) {
				continue // unchanged; say nothing
			}
			o := old
			actions = append(actions, buildAction{Verb: "update", Topic: d.Topic, Kind: d.Kind, Body: d.Body, Prev: &o, Src: d.Sources, Tags: d.tags()})
			continue
		}
		actions = append(actions, buildAction{Verb: "add", Topic: d.Topic, Kind: d.Kind, Body: d.Body, Src: d.Sources, Tags: d.tags()})
	}
	for topic, old := range prev {
		// Questions are not derived from evidence, so they never go stale; only a person
		// removes one, by answering it.
		if !seen[topic] && old.Kind != "question" {
			o := old
			actions = append(actions, buildAction{Verb: "remove", Topic: topic, Kind: old.Kind, Body: old.Body, Prev: &o})
		}
	}
	sort.Slice(actions, func(a, b int) bool {
		if actions[a].Verb != actions[b].Verb {
			return actions[a].Verb < actions[b].Verb
		}
		return actions[a].Topic < actions[b].Topic
	})
	return actions, nil
}

func hasTag(f fact, tag string) bool {
	for _, t := range f.Tags {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return true
		}
	}
	return false
}

func topicOf(f fact) string {
	for _, t := range f.Tags {
		if s := strings.TrimSpace(t); strings.HasPrefix(s, topicPrefix) {
			return strings.TrimPrefix(s, topicPrefix)
		}
	}
	return ""
}

// memoryBuildCmd is `ptln memory build [--apply] [--remove-stale] [--refresh]`.
//
// A dry run by DEFAULT. The plan's rule is propose, don't commit: a seed is bulk curation, and the
// one thing worse than no facts is a pile of unreviewed ones that everybody then stops reading.
func memoryBuildCmd(args []string) {
	apply, removeStale, refresh := false, false, false
	for _, a := range args {
		switch a {
		case "--apply":
			apply = true
		case "--refresh":
			refresh = true
		case "--remove-stale":
			removeStale = true
		}
	}
	ws, _ := workspaceHere()
	if !ws.repoLocal {
		fatal(fmt.Errorf("build reads the code repo it is run in; this is a legacy side-repo project"))
	}
	actions, err := planBuild(ws.Dir)
	if err != nil {
		fatal(err)
	}
	facts, _ := readFacts(ws.Dir, false)
	printMemoryDrift(ws.Dir, facts)
	_, hasOverview := currentOverview(ws.Dir)
	if len(actions) == 0 {
		if apply && (!hasOverview || refresh) {
			ensureOverview(ws, refresh)
			refreshMemoryMap(ws)
			return
		}
		if refresh && hasOverview {
			fmt.Println("~ overview         rewritten on --apply from the last commit, starting from the current one")
			return
		}
		fmt.Println("nothing to change — the derived facts already match the repo")
		return
	}

	var added, updated, removed int
	for _, a := range actions {
		switch a.Verb {
		case "add":
			fmt.Printf("+ %-16s %s\n", a.Topic, clipVis(a.Body, 92))
			if len(a.Src) > 0 {
				fmt.Printf("  %-16s evidence: %s\n", "", strings.Join(a.Src, ", "))
			}
			added++
		case "update":
			fmt.Printf("~ %-16s %s\n", a.Topic, clipVis(a.Body, 92))
			fmt.Printf("  %-16s was: %s\n", "", clipVis(a.Prev.Body, 92))
			updated++
		case "remove":
			fmt.Printf("- %-16s %s\n", a.Topic, clipVis(a.Body, 92))
			removed++
		}
	}
	if refresh && hasOverview {
		fmt.Printf("~ %-16s %s\n", "overview", "rewritten on --apply from the last commit, starting from the current one")
	}
	if !hasOverview {
		fmt.Printf("+ %-16s %s\n", "overview", "a description of the project, generated on --apply by claude reading the repository")
	}
	fmt.Printf("\n%d to add, %d to update, %d stale\n", added, updated, removed)

	if !apply {
		hint := "ptln memory build --apply"
		if refresh {
			hint += " --refresh"
		}
		fmt.Printf("\nnothing written. `%s` to record these.\n", hint)
		if removed > 0 {
			fmt.Println("stale derived facts are left alone unless you add --remove-stale.")
		}
		return
	}

	// Checkpoint as it goes: a deep pass over a legacy repo is a long run, and one fact per file
	// means an interrupted run still leaves everything it had already established.
	var wrote int
	for _, a := range actions {
		switch a.Verb {
		case "add":
			f := fact{Kind: a.Kind, By: memoryAuthor(), At: time.Now(), Body: a.Body, Tags: a.Tags, Sources: a.Src}
			if _, err := writeFact(ws.Dir, f); err != nil {
				fmt.Printf("  ! %s: %v\n", a.Topic, err)
				continue
			}
			wrote++
		case "update":
			if _, err := rewriteFact(ws.Dir, a.Prev.ID, a.Body, memoryAuthor()); err != nil {
				fmt.Printf("  ! %s: %v\n", a.Topic, err)
				continue
			}
			wrote++
		case "remove":
			if !removeStale {
				continue
			}
			if _, err := removeFact(ws.Dir, a.Prev.ID); err != nil {
				fmt.Printf("  ! %s: %v\n", a.Topic, err)
				continue
			}
			wrote++
		}
	}
	fmt.Printf("\n✓ %d fact(s) written to %s\n", wrote, memoryDir(ws.Dir))
	ensureOverview(ws, refresh)
	refreshMemoryMap(ws)
	fmt.Println("  review them with `git diff` before you commit — they are proposals, not gospel")
}

// makeTargets lists the phony-ish targets a Makefile declares. Deliberately crude: it reads the
// target lines, not the recipes, because the useful fact is "these commands exist", and anything
// cleverer would mean implementing make.
func makeTargets(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == '.' || line[0] == ' ' {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 || strings.HasPrefix(line, "include") {
			continue
		}
		// Skip variable assignments (FOO := bar, FOO ?= bar).
		if i+1 < len(line) && (line[i+1] == '=') {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if name == "" || strings.ContainsAny(name, " \t$%") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

// jsonKeys returns the keys of one top-level object in a JSON file, sorted.
func jsonKeys(path, field string) []string {
	m, err := readJSONFile(path)
	if err != nil {
		return nil
	}
	obj, _ := m[field].(map[string]any)
	if obj == nil {
		return nil
	}
	var out []string
	for k := range obj {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}
