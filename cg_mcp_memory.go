package main

// The MCP half of project memory — which is the half that matters, because the intended user of
// this system is the agent in the session, not a person typing `ptln memory add`.
//
// Three tools: read what the team knows, record something durable, and set the project up when
// it isn't. They are deliberately few. A large tool surface makes an agent choose badly; these
// are the only operations the loop needs.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/gitwt"
)

// memoryTools are appended to the context-threads MCP tool list.
var memoryTools = []map[string]any{
	{
		"name":        "project_memory",
		"description": "WHAT THE TEAM HAS LEARNED about this project — decisions, constraints, contracts and gotchas recorded by everyone's sessions, across ALL the project's repositories, not just this one. Read it before answering anything about how the project works, before proposing a change to shared behaviour, and whenever you are about to guess. It is the answer to \"why is it like this\" without asking a human to paste a transcript from another session.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"all": map[string]any{"type": "boolean", "description": "include superseded facts (history). Default false — current beliefs only."},
			},
		},
	},
	{
		"name":        "remember",
		"description": "RECORD SOMETHING DURABLE the team must not have to rediscover: a decision and why, a constraint the code must respect, an interface contract, or a gotcha that cost time. Writes one file to the project's shared memory and syncs it, so teammates' sessions read it at their next start — this is what replaces pasting a session transcript into chat. Record the durable conclusion, never the conversation. If it replaces an earlier fact, pass supersedes with that fact's id, so the old belief stops being briefed.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":       map[string]any{"type": "string", "enum": factKinds, "description": "decision | constraint | contract | gotcha | question"},
				"body":       map[string]any{"type": "string", "description": "one or two sentences, in the present tense, stating the fact and (for a decision) why."},
				"repo":       map[string]any{"type": "string", "description": "the project repo this is about. Omit when it is true of the whole project."},
				"supersedes": map[string]any{"type": "string", "description": "the id of the fact this replaces."},
				"tags":       map[string]any{"type": "string", "description": "comma-separated subjects, e.g. \"deploy, auth\". Facts that share a tag and disagree are surfaced as a conflict."},
			},
			"required": []string{"kind", "body"},
		},
	},
	{
		"name":        "propose_fact",
		"description": "BACKFILL A FACT MINED FROM SOMEWHERE ELSE — a ticket, a session log, an old commit, a chat thread. Use this instead of remember when you did not witness the thing yourself but found it in a source you can cite. Every proposal MUST cite its sources; a claim a reviewer cannot check is worse than no claim. Proposed facts are marked as inferred and briefed BELOW facts a person recorded, so backfilling cannot dilute what the team actually decided. Prefer things corroborated by more than one independent source — a decision that appears in a commit AND a ticket is real; one that appears once in a session log is usually chatter. Do not propose transient state (build results, test failures now fixed, work in progress); propose only what will still be true in a month.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":    map[string]any{"type": "string", "enum": factKinds, "description": "decision | constraint | contract | gotcha | question"},
				"body":    map[string]any{"type": "string", "description": "one or two sentences stating what is durably true."},
				"sources": map[string]any{"type": "string", "description": "comma-separated citations as <system>:<id>, e.g. \"odoo:ACR-1412, commit:4a50eb9\". Required."},
				"repo":    map[string]any{"type": "string", "description": "the project repo this is about. Omit when it is true of the whole project."},
			},
			"required": []string{"kind", "body", "sources"},
		},
	},
	{
		"name":        "setup_project",
		"description": "SET THIS REPO UP AS PART OF A PROJECT — call it when project_memory says the repo belongs to no project and the user wants shared memory. A project spans MANY repositories (a fleet manager, an integration codebase, the services around them) and has one memory home all of them share, which is an ordinary git repo. Pass label to create or join by name; pass memory_remote to point at a teammate's existing project. Tell the user plainly what you did: the repo now carries a .partyline.json they should commit, and facts recorded here reach everyone who has that memory repo.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label":         map[string]any{"type": "string", "description": "the project name, e.g. \"acr\". Required when creating."},
				"memory_remote": map[string]any{"type": "string", "description": "git URL of the project's memory repo. Given: join it. Omitted: create the project locally, to be shared later."},
				"repo_name":     map[string]any{"type": "string", "description": "short handle for THIS repository inside the project. Defaults to the directory name."},
			},
		},
	},
}

// handleProjectMemory answers with the brief plus the full current list, so the agent has both
// the ranked summary and the detail without a second call.
// toolArgs pulls the raw `arguments` object out of a tools/call request. The shared params
// struct in cg_mcp.go is typed for the older tools, so these parse their own.
func toolArgs(req rpcReq) json.RawMessage {
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	return p.Arguments
}

func (s *cgServer) handleProjectMemory(enc *json.Encoder, req rpcReq) {
	args := toolArgs(req)
	var a struct {
		All bool `json:"all"`
	}
	_ = json.Unmarshal(args, &a)
	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		s.toolResult(enc, req.ID, "This repository is not part of a project yet, so there is no shared memory to read. If the user wants one, call setup_project.", false)
		return
	}
	stale := ""
	if _, err := memoryPull(ws.Dir); err != nil {
		// Say so and continue: stale memory beats no memory, but silence about staleness is how
		// an agent confidently answers from last week's beliefs.
		stale = fmt.Sprintf("\n(could not fetch teammates' latest: %v — this may be out of date)\n", err)
	}
	facts, err := readFacts(ws.Dir, a.All)
	if err != nil {
		s.toolResult(enc, req.ID, fmt.Sprintf("could not read the project's memory: %v", err), true)
		return
	}
	if len(facts) == 0 {
		s.toolResult(enc, req.ID, fmt.Sprintf("Project %q has nothing recorded yet. Record the first durable thing you learn with remember.", ws.Label), false)
		return
	}
	s.toolResult(enc, req.ID, stale+renderBrief(ws.Label, ws.repoNameFor(cwd), facts, 0), false)
}

// handleRemember writes one fact and syncs it.
func (s *cgServer) handleRemember(enc *json.Encoder, req rpcReq) {
	args := toolArgs(req)
	var a struct {
		Kind, Body, Repo, Supersedes, Tags string
	}
	_ = json.Unmarshal(args, &a)
	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		s.toolResult(enc, req.ID, "Nothing to record into: this repository is not part of a project. Call setup_project first.", true)
		return
	}
	if !validFactKind(a.Kind) {
		s.toolResult(enc, req.ID, "kind must be one of: "+strings.Join(factKinds, ", "), true)
		return
	}
	repo := a.Repo
	if repo == "" {
		repo = ws.repoNameFor(cwd)
	}
	f := fact{Kind: a.Kind, Repo: repo, By: memoryAuthor(), At: time.Now(), Supersedes: a.Supersedes, Body: a.Body}
	for _, t := range strings.Split(a.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			f.Tags = append(f.Tags, t)
		}
	}
	if _, err := writeFact(ws.Dir, f); err != nil {
		s.toolResult(enc, req.ID, fmt.Sprintf("could not record it: %v", err), true)
		return
	}
	if err := memorySync(ws.Dir, "memory: "+a.Kind+" — "+clipVis(oneLine(a.Body), 60)); err != nil {
		s.toolResult(enc, req.ID, fmt.Sprintf("Recorded locally, but NOT shared yet: %v. Tell the user their teammates will not see this until `ptln memory sync` succeeds.", err), false)
		return
	}
	pokeStatus()
	s.toolResult(enc, req.ID, "Recorded and shared. Teammates' sessions pick it up on their next turn.", false)
}

// handleProposeFact records a fact mined from another system. Unlike remember, it demands
// citations and marks what it writes as inferred — see memory_propose.go for why both matter.
func (s *cgServer) handleProposeFact(enc *json.Encoder, req rpcReq) {
	args := toolArgs(req)
	var a struct {
		Kind, Body, Sources string
		// A POINTER, so "omitted" is distinguishable from "". The tool description says to omit
		// this when a fact is true of the whole project; with a plain string that read as empty
		// and fell back to whatever repo the session happened to be sitting in, which filed
		// cross-repo facts — a Java lane client key, a POS library shadowing — under the cloud
		// service. The description was right and the code was not.
		Repo *string
	}
	_ = json.Unmarshal(args, &a)
	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		s.toolResult(enc, req.ID, "Nothing to record into: this repository is not part of a project. Call setup_project first.", true)
		return
	}
	if !validFactKind(a.Kind) {
		s.toolResult(enc, req.ID, "kind must be one of: "+strings.Join(factKinds, ", "), true)
		return
	}
	var sources []string
	for _, t := range strings.Split(a.Sources, ",") {
		if t = strings.TrimSpace(t); t != "" {
			if !validSourceRef(t) {
				s.toolResult(enc, req.ID, fmt.Sprintf("citation %q is not <system>:<id> — e.g. odoo:ACR-1412, commit:4a50eb9", t), true)
				return
			}
			sources = append(sources, t)
		}
	}
	if len(sources) == 0 {
		s.toolResult(enc, req.ID, "A proposed fact needs at least one citation. If you witnessed this yourself rather than finding it in a source, use remember instead.", true)
		return
	}
	existing, err := readFacts(ws.Dir, true)
	if err != nil {
		s.toolResult(enc, req.ID, fmt.Sprintf("could not read what is already known: %v", err), true)
		return
	}
	if dup, found := dedupeAgainst(existing, a.Body); found {
		s.toolResult(enc, req.ID, fmt.Sprintf("Already known as %s (recorded by %s) — nothing written.", dup.ID, dup.By), false)
		return
	}
	repo := ""
	if a.Repo != nil {
		repo = strings.TrimSpace(*a.Repo)
	}
	// Mint the id HERE. writeFact takes the fact by value and mints one internally, so the
	// caller's copy kept an empty id and every confirmation read "Proposed , citing ..." — which
	// left an agent with no handle to supersede or correct what it had just written.
	now := time.Now()
	f := fact{ID: newFactID(now), Kind: a.Kind, Repo: repo, By: proposedBy, At: now, Sources: sources, Body: a.Body}
	if _, err := writeFact(ws.Dir, f); err != nil {
		s.toolResult(enc, req.ID, fmt.Sprintf("could not record it: %v", err), true)
		return
	}
	scope := "the whole project"
	if repo != "" {
		scope = repo
	}
	// SHARE IT. This used to stop at the local write and tell the caller to run `ptln memory
	// sync` — but nothing prompts that, so proposals sat invisible to teammates and nothing
	// reached the activity feed. Proposals are already marked and ranked below authored facts;
	// that is what makes publishing them safe, and deleting the file is the way to reject one.
	msg := fmt.Sprintf("Proposed %s about %s, citing %s. Marked as inferred, so it is briefed below facts a person recorded. To correct it, propose the replacement and delete %s.md from the memory repo.",
		f.ID, scope, strings.Join(sources, ", "), f.ID)
	if err := memorySync(ws.Dir, "memory: proposed "+a.Kind+" — "+clipVis(oneLine(a.Body), 50)); err != nil {
		s.toolResult(enc, req.ID, msg+fmt.Sprintf(" NOT shared yet: %v — teammates will not see it until `ptln memory sync` succeeds.", err), false)
		return
	}
	pokeStatus()
	s.toolResult(enc, req.ID, msg+" Shared — teammates' sessions pick it up on their next turn.", false)
}

// handleSetupProjectMemory is the conversational setup path: the agent, not a wizard, puts this
// repo into a project. Creating and joining are one verb because from the user's side they are
// one intent — "make this repo part of our project" — and which one it is depends only on
// whether a teammate got there first.
func (s *cgServer) handleSetupProjectMemory(enc *json.Encoder, req rpcReq) {
	var a struct {
		Label        string `json:"label"`
		MemoryRemote string `json:"memory_remote"`
		RepoName     string `json:"repo_name"`
	}
	_ = json.Unmarshal(toolArgs(req), &a)
	cwd, _ := os.Getwd()
	repo, err := gitRepoRootOrEmpty(cwd)
	if err != nil {
		s.toolResult(enc, req.ID, "This directory is not a git repository. A project member is a repo, so there is nothing to add.", true)
		return
	}
	remote := gitOriginURL(repo)
	if remote == "" {
		s.toolResult(enc, req.ID, "This repo has no `origin` remote. A project member is identified by its remote — a path means a different repo on a teammate's machine. Ask the user to add one.", true)
		return
	}

	label := strings.TrimSpace(a.Label)
	if a.MemoryRemote != "" {
		if label == "" {
			label = strings.TrimSuffix(filepath.Base(strings.TrimSuffix(a.MemoryRemote, "/")), ".git")
		}
		if _, err := loadProjectWorkspace(label); err != nil {
			if out, cerr := gitMem(projectsRoot(), "clone", "--quiet", a.MemoryRemote, projectWorkspaceDir(label)); cerr != nil {
				_ = os.MkdirAll(projectsRoot(), 0o755)
				if out2, cerr2 := gitMem(projectsRoot(), "clone", "--quiet", a.MemoryRemote, projectWorkspaceDir(label)); cerr2 != nil {
					s.toolResult(enc, req.ID, fmt.Sprintf("could not clone the project's memory repo: %s", firstLineOf(out+out2)), true)
					return
				}
			}
		}
	}
	if label == "" {
		s.toolResult(enc, req.ID, "Which project? Ask the user for a name (e.g. \"acr\"), or for the git URL of the project's memory repo if a teammate already made one.", true)
		return
	}

	ws, err := loadProjectWorkspace(label)
	if err != nil {
		// Creating: local first, shareable the moment a remote exists. A project nobody has
		// pushed yet is a normal state, not an error.
		dir := projectWorkspaceDir(label)
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not create the project: %v", mkErr), true)
			return
		}
		if _, gErr := gitMem(dir, "rev-parse", "--git-dir"); gErr != nil {
			if out, iErr := gitMem(dir, "init", "-q"); iErr != nil {
				s.toolResult(enc, req.ID, fmt.Sprintf("git init failed: %s", firstLineOf(out)), true)
				return
			}
		}
		if sErr := saveProjectFile(dir, projectFile{Label: label}); sErr != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not write the project file: %v", sErr), true)
			return
		}
		ws, _ = loadProjectWorkspace(label)
	}
	if ws == nil {
		s.toolResult(enc, req.ID, "could not open the project after creating it", true)
		return
	}

	name := strings.TrimSpace(a.RepoName)
	if name == "" {
		name = filepath.Base(repo)
	}
	already := false
	for _, r := range ws.File.Repos {
		if sameProjectRepo(r.Remote, remote) {
			already, name = true, r.Name
			break
		}
	}
	if !already {
		ws.File.Repos = append(ws.File.Repos, struct {
			Name   string `json:"name"`
			Remote string `json:"remote"`
			Note   string `json:"note,omitempty"`
		}{Name: name, Remote: remote})
		if err := saveProjectFile(ws.Dir, ws.File); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not record the repo: %v", err), true)
			return
		}
	}
	writeRepoProject(repo, ws.Label)

	var out strings.Builder
	fmt.Fprintf(&out, "This repo (%s) is part of project %q, which has %d repo(s).\n", name, ws.Label, len(ws.File.Repos))
	if err := memorySync(ws.Dir, "project: add repo "+name); err != nil {
		fmt.Fprintf(&out, "\nIt is NOT shared with teammates yet: %v\n", err)
		fmt.Fprintf(&out, "Tell the user the project's memory repo needs a remote:\n  git -C %s remote add origin <git-url> && ptln memory sync\n", ws.Dir)
	} else {
		out.WriteString("\nShared — teammates who join this project read what you record here.\n")
	}
	out.WriteString("\nTell the user to commit .partyline.json in this repo, so anyone who clones it inherits the link. Then record what the team already knows with remember.")
	s.toolResult(enc, req.ID, out.String(), false)
}

// gitRepoRootOrEmpty keeps the import surface of this file small.
func gitRepoRootOrEmpty(dir string) (string, error) { return gitwt.RepoRoot(dir) }
