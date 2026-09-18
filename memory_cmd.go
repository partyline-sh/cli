package main

// `ptln memory` and the project verbs that feed it. Deliberately small: the intended way to use
// any of this is to ask the agent in the session you are already in (see the MCP tools in
// cg_mcp.go). These exist so a human can inspect and repair what the agents wrote, and so the
// whole thing is scriptable without a browser.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/gitwt"
)

func memoryMain(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
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
	case "watch":
		memoryWatchMain(args)
	default:
		fatal(fmt.Errorf("ptln memory: unknown %q — add | ls | brief | sync", sub))
	}
}

// workspaceHere resolves the project from the working directory, or explains how to get one.
func workspaceHere() (*projectWorkspace, string) {
	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		fatal(fmt.Errorf("this repo is not part of a project yet — ask your agent to set it up, or run `ptln project join <git-url>` then `ptln project add-repo`"))
	}
	return ws, ws.repoNameFor(cwd)
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
	if err := memorySync(ws.Dir, "memory: "+kind+" — "+clipVis(oneLine(body), 60)); err != nil {
		fmt.Printf("  saved here, but NOT shared yet: %v\n", err)
		fmt.Println("  fix the remote and run: ptln memory sync")
		return
	}
	pokeStatus() // the bar reflects it before the human's next keystroke
	fmt.Println("  shared — your teammates' sessions will read it")
}

func memoryList(args []string) {
	all := len(args) > 0 && args[0] == "--all"
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
	out := renderBrief(ws.Label, here, facts)
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
