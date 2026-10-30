package main

// `ptln memory` and the project verbs that feed it. Deliberately small: the intended way to use
// any of this is through the agent in the session you are already in, which runs these same
// commands — the AGENTS.md block tells it how, and in a repo with no memory yet the per-turn hook
// offers setup (memory_offer.go). These exist so a human can inspect and repair what the agents
// wrote, and so the whole thing is scriptable.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/gitwt"

	"partyline.sh/partyline/internal/clispec"
)

func memoryMain(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	// `ptln memory add --help` once took "--help" as the kind and failed with a list nobody had
	// been shown. Any subcommand's --help prints the memory help, which lists every kind.
	// Only flags before the first word count: a fact body can be unquoted words, and
	// `add gotcha grep -h drops filenames` is a fact about grep, not a request for help.
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			break
		}
		if a == "--help" || a == "-h" {
			if spec, ok := clispec.Lookup("memory"); ok {
				clispec.PrintHelp(os.Stdout, spec)
			}
			return
		}
	}
	switch sub {
	case "add":
		memoryAdd(args)
	case "ls", "list", "":
		memoryList(args)
	case "brief":
		memoryBrief(args)
	case "sync":
		memorySyncCmd()
	case "harvest":
		memoryHarvestMain(args)
	case "propose":
		memoryProposeMain(args)
	case "edit", "rewrite":
		memoryEdit(args)
	case "append":
		memoryAppend(args)
	case "verify":
		memoryVerify(args)
	case "rm", "remove", "forget":
		memoryRemove(args)
	case "where":
		memoryWhere()
	case "hooks":
		memoryHooksCmd(args)
	case "build":
		memoryBuildCmd(args)
	case "map":
		memoryMapCmd()
	default:
		fatal(fmt.Errorf("ptln memory: unknown %q — add | ls | brief | edit | append | rm | where | map | hooks | build | sync | harvest | propose", sub))
	}
}

// workspaceHere resolves where this directory's facts live.
//
// REPO-LOCAL FIRST. The default is `<repo>/memory`, so a checkout carries what the team learned
// about it and no setup stands between a person and their first fact. A legacy side-repo project
// still resolves, so machines already using one keep working until their facts are moved in.
//
// It resolves through memoryTarget first, the same rule the hooks and capture use, so a fact an
// agent records lands where the next session is briefed from. A repo still bound to a legacy
// project and with no memory/ of its own therefore keeps writing to the project.
func workspaceHere() (*projectWorkspace, string) {
	cwd, _ := os.Getwd()
	if dir, label, repo, ok := memoryTarget(cwd); ok {
		if isRepoLocalMemory(dir) {
			return &projectWorkspace{Dir: dir, Label: label, repoLocal: true}, repo
		}
		if ws, ok := projectForDir(cwd); ok {
			return ws, repo
		}
	}
	if root, ok := repoMemory(cwd); ok {
		// The repo IS the workspace. No label, no project file, no second remote: the store
		// functions take a workspace dir and append `memory/`, so the repo root is all they need.
		return &projectWorkspace{Dir: root, Label: filepath.Base(root), repoLocal: true}, ""
	}
	fatal(fmt.Errorf("not a git repository — repo-local memory lives in `<repo>/memory`, so run this inside one"))
	return nil, ""
}

func memoryAdd(args []string) {
	kind, body := "", ""
	repo, supersedes, tags := "", "", ""
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			if i+1 < len(args) {
				repo, i = args[i+1], i+1
			}
		case "--supersedes":
			if i+1 < len(args) {
				supersedes, i = args[i+1], i+1
			}
		case "--tags":
			if i+1 < len(args) {
				tags, i = args[i+1], i+1
			}
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) >= 2 {
		kind, body = rest[0], strings.Join(rest[1:], " ")
	}
	if kind == "" || body == "" {
		fatal(fmt.Errorf("usage: ptln memory add <%s> \"what was learned\" [--repo <name>] [--supersedes <id>] [--tags a,b]",
			strings.Join(factKinds, "|")))
	}
	ws, here := workspaceHere()
	if kind == "overview" {
		if cur, ok := currentOverview(ws.Dir); ok {
			fatal(fmt.Errorf("this repo already has an overview (%s) — change it with `ptln memory edit %s \"...\"`", cur.ID, cur.ID))
		}
	}
	if repo == "" {
		repo = here // a fact recorded inside a repo is about that repo unless told otherwise
	}
	f := fact{Kind: kind, Repo: repo, By: memoryAuthor(), At: time.Now(), Supersedes: supersedes, Body: body}
	for _, t := range strings.Split(tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			f.Tags = append(f.Tags, t)
		}
	}
	path, err := writeFact(ws.Dir, f)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ recorded %s\n", filepath.Base(path))
	// Repo-local: the fact is a change in YOUR working tree and there is nothing to push. Saying
	// "not shared yet — fix the remote" here would be telling someone to add a remote to their own
	// repo for a sync that is a deliberate no-op.
	if ws.repoLocal {
		f.ID = strings.TrimSuffix(filepath.Base(path), ".md")
		refreshMemoryMap(ws)
		reportUncommitted(ws, f)
		pokeStatus()
		return
	}
	if err := memorySync(ws.Dir, "memory: "+kind+" — "+clipVis(oneLine(body), 60)); err != nil {
		fmt.Printf("  saved here, but NOT shared yet: %v\n", err)
		fmt.Println("  fix the remote and run: ptln memory sync")
		return
	}
	pokeStatus() // the bar reflects it before the human's next keystroke
	// Only claim it reached anyone when there is somewhere for it to have gone. A project with
	// no remote yet syncs "successfully" by doing nothing, and saying "shared" there is a false
	// statement at exactly the moment someone is deciding whether to trust this at all.
	if !hasUpstream(ws.Dir) {
		fmt.Println("  saved here — this project has no remote yet, so nobody else can see it")
		fmt.Printf("  git -C %s remote add origin <git-url> && ptln memory sync\n", ws.Dir)
		return
	}
	fmt.Println("  shared — your teammates' sessions will read it")
}

func memoryList(args []string) {
	// An unrecognised flag used to be IGNORED, so `ptln memory ls --help` printed the fact list
	// and anyone who mistyped a flag got a confident answer to a question they had not asked.
	all := false
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		case "-h", "--help", "help":
			fmt.Println("ptln memory ls [--all] — every current fact; --all includes superseded ones")
			return
		default:
			fatal(fmt.Errorf("ptln memory ls: unknown argument %q (usage: ptln memory ls [--all])", a))
		}
	}
	ws, _ := workspaceHere()
	facts, err := readFacts(ws.Dir, all)
	if err != nil {
		fatal(err)
	}
	if len(facts) == 0 {
		fmt.Println("nothing recorded yet")
		return
	}
	for _, f := range facts {
		scope := "project"
		if f.Repo != "" {
			scope = f.Repo
		}
		fmt.Printf("%-10s %-16s %s\n", f.Kind, scope, oneLine(f.Body))
		fmt.Printf("%-10s %-16s %s · %s\n", "", "", f.By+", "+humanAge(f.At), f.ID)
	}
}

func memoryBrief(_ []string) {
	ws, here := workspaceHere()
	facts, err := readFacts(ws.Dir, false)
	if err != nil {
		fatal(err)
	}
	out := renderBrief(ws.Label, here, facts, briefMax)
	if out == "" {
		fmt.Println("nothing recorded yet")
		return
	}
	fmt.Print(out)
}

func memorySyncCmd() {
	ws, _ := workspaceHere()
	if err := memorySync(ws.Dir, "memory: sync"); err != nil {
		fatal(err)
	}
	fmt.Println("✓ in sync")
}

// memoryAuthor names who learned it. The git identity, because that is who the teammate reading
// the brief will recognise — not a partyline handle they have never seen.
func memoryAuthor() string {
	cwd, _ := os.Getwd()
	if repo, err := gitwt.RepoRoot(cwd); err == nil {
		if name, err := gitMem(repo, "config", "user.name"); err == nil && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "someone"
}

// memoryEdit rewrites a fact in place. See memory_mutate.go for why editing beats appending a
// correction: the previous store briefed a wrong claim and its correction side by side.
func memoryEdit(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: ptln memory edit <id> \"the corrected fact\""))
	}
	ws, _ := workspaceHere()
	f, err := rewriteFact(ws.Dir, args[0], strings.Join(args[1:], " "), memoryAuthor())
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ rewrote %s (%s)\n", f.ID, f.Kind)
	refreshMemoryMap(ws)
	reportUncommitted(ws, f)
}

// memoryAppend adds to a fact a session has learned more about.
func memoryAppend(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: ptln memory append <id> \"what else is true\""))
	}
	ws, _ := workspaceHere()
	f, err := appendToFact(ws.Dir, args[0], strings.Join(args[1:], " "), memoryAuthor())
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ appended to %s (%s)\n", f.ID, f.Kind)
	refreshMemoryMap(ws)
	reportUncommitted(ws, f)
}

// memoryRemove deletes a fact's file.
func memoryRemove(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln memory rm <id>"))
	}
	ws, _ := workspaceHere()
	f, err := removeFact(ws.Dir, args[0])
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ removed %s — %s\n", f.ID, clipVis(oneLine(f.Body), 60))
	refreshMemoryMap(ws)
	if ws.repoLocal {
		fmt.Println("  the deletion is in your working tree; `git status` shows it before you commit")
	}
}

// memoryWhere answers "where do this directory's facts live", which is the first thing anyone asks
// when a brief is empty.
func memoryWhere() {
	ws, _ := workspaceHere()
	facts, _ := readFacts(ws.Dir, false)
	mode := "side repo (legacy)"
	if ws.repoLocal {
		mode = "in the repo"
	}
	fmt.Printf("%s\n", memoryDir(ws.Dir))
	fmt.Printf("  mode     %s\n", mode)
	fmt.Printf("  facts    %d\n", len(facts))
	if ws.repoLocal {
		fmt.Println("  commit   with your code — partyline never commits your repo")
	}
}

// reportUncommitted tells the truth about what just happened to the working tree. Silence here
// would read as "saved and shared", which is exactly wrong for a repo partyline does not own.
func reportUncommitted(ws *projectWorkspace, f fact) {
	if !ws.repoLocal {
		return
	}
	rel := f.ID + ".md"
	if p, err := filepath.Rel(ws.Dir, filepath.Join(memoryDir(ws.Dir), f.ID+".md")); err == nil {
		rel = p
	}
	fmt.Printf("  %s — uncommitted; commit it with the change that taught it\n", rel)
}
