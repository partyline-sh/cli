package main

// cg_create_project.go — turn the repo a CLI session is sitting in into a partyline PROJECT,
// without the web app. Backs the `create_project` MCP tool and `ptln project setup`.
//
// THE HOLE THIS FILLS. planning_open refuses in an unregistered repo, and nothing in the CLI created
// a project. The one auto-create that exists (#586, resolveThreadForWrite) is deliberately narrow —
// it fires only on `remember`, and it makes a THREAD, not a project. So a session that had just
// worked out what to build had to stop, open the web app, and set the project up by hand.
//
// FOUR STEPS, ONE SUMMARY — and they are not interchangeable:
//
//  1. the PROJECT is the org-side identity work is filed against;
//  2. the THREAD (pinned in .partyline.json) is the shared memory teammates who pull land in;
//  3. the in-process REBIND is why planning_open works in the same session, with no relaunch;
//  4. the local REGISTRY entry (label → absolute path) is what makes the label resolvable to a
//     directory on THIS machine — without it promote_work_item refuses, because nothing advertises
//     the label.
//
// Step 4 is a CONSENT boundary, not bookkeeping. Per launchPolicy, REGISTRATION IS THE CONSENT: it
// declares this directory available to the team, and agents they dispatch may build in it
// unattended. So it is stated out loud in the summary, with how to undo it. Never silent.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// projectSetup is what actually happened, so the caller can report it truthfully — including a
// PARTIAL setup. "Created the project but could not register it" is a real outcome (planning works,
// promote will not), and a half-finished setup that reads as complete is worse than a clear partial.
type projectSetup struct {
	Label     string // the project label — also the local path lookup key
	ProjectID string
	Thread    string
	Path      string // absolute repo root, local only (never leaves the machine)

	MadeProject bool // false → adopted one the org already had
	MadeThread  bool
	WrotePin    bool

	Registered  bool
	RegisterErr string // non-empty when the label is NOT resolvable to this dir on this machine
}

// createProjectHere is the whole four-step setup, split from the MCP handler so it can be tested and
// reused by `ptln project setup`. Returns (setup, message, isError); on a refusal setup is nil and
// the message carries the REASON — an error an agent cannot act on costs it a whole turn.
func createProjectHere(c *api.Client, want string) (*projectSetup, string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "Could not read the working directory: " + err.Error(), true
	}
	root, rerr := gitwt.RepoRoot(cwd)
	if rerr != nil {
		return nil, fmt.Sprintf("%s is not a git repository, so there is nothing stable to identify a project by. "+
			"Run `git init` and add an `origin` remote (or cd into the repo), then call create_project again.", cwd), true
	}
	// A local path is NOT an identity: the same path on another machine is a different repo, and a
	// project keyed on one would resolve to whatever happens to sit there. The origin remote is the
	// only thing that means the same repo everywhere, so a repo without one is refused by name.
	remote := gitOriginURL(root)
	if remote == "" {
		return nil, fmt.Sprintf("%s has no `origin` remote. A project is keyed on the repo's remote, because a local "+
			"path means a different repo on someone else's machine. Add one (`git remote add origin <url>`) and "+
			"call create_project again.", root), true
	}
	if api.LoadToken() == "" {
		return nil, "Not signed in on this machine, so there is no account to create the project in — run `ptln login` " +
			"in a terminal, then call create_project again.", true
	}

	label := strings.TrimSpace(want)
	if label == "" {
		label = filepath.Base(root)
	}
	// The label becomes a lookup key in the local registry (and rides to the control plane as data).
	// It is never interpolated into an argv or a shell string — reference, not command — but it is
	// still validated to the shape every other registration uses, so one label means one thing.
	if !labelRe.MatchString(label) {
		return nil, fmt.Sprintf("%q is not a usable project label (letters/digits/space/._- , at most 48 characters). "+
			"Pass `label` with a simpler name — it becomes the handle machines advertise.", label), true
	}

	set := &projectSetup{Label: label, Path: root, Thread: loadRepoBind(root)}
	stamped := false // whether the project already carries this repo's identity

	// ---- THE DUPLICATE GUARD ---------------------------------------------------------------------
	// Asked of the server, which owns remote normalization (ssh vs https clone = one repo). If the org
	// already has this repo, we ADOPT that project and never make a second one — a duplicate project
	// is not a cosmetic problem: two labels for one repo split the backlog, the runs and the board.
	existingLabel, existingThread, _, lerr := c.ResolveRepo(remote)
	if lerr != nil {
		return nil, "Could not check whether this repo is already a project (nothing was created): " + lerr.Error(), true
	}
	if existingLabel != "" {
		set.Label = existingLabel
		if set.Thread == "" {
			set.Thread = existingThread
		}
	} else {
		// Not known by remote. A project may still exist under this LABEL — created from the web
		// before anything was stamped with a repo. Adopt that one rather than colliding with it.
		projects, perr := c.ListProjects()
		if perr != nil {
			return nil, "Could not list your projects to check for an existing one (nothing was created): " + perr.Error(), true
		}
		existing := projectWithLabel(projects, label)
		if existing != nil {
			// Which repo does that project belong to? COMPARE IT TO OURS — do not infer "different
			// repo" from "has one at all". The server declining to match is not proof of a different
			// repo: /threads/resolve answers about the THREAD, so a project of ours whose plan thread
			// is missing comes back as no match, and treating that as a collision would refuse to set
			// up a repo against its own project, quoting its own remote back as someone else's.
			//
			// A genuine mismatch still refuses: stealing the label would point the team's runs at the
			// wrong checkout.
			if url := strings.TrimSpace(existing.RepoURL); url != "" && !sameRemote(url, remote) {
				return nil, fmt.Sprintf("Your org already has a project called %q, and it belongs to a different repo:\n"+
					"  that project: %s\n  this checkout: %s\nPass `label` with a different name for this one.",
					label, url, remote), true
			}
			set.ProjectID = existing.ID
			// Already stamped with this same repo, just spelled differently — leave the team's
			// spelling alone rather than rewriting it to ours for no gain.
			stamped = sameRemote(existing.RepoURL, remote)
		}
	}

	// ---- 1. the project --------------------------------------------------------------------------
	if set.ProjectID == "" && existingLabel == "" {
		p, cerr := c.CreateProject(label, "")
		if cerr != nil {
			return nil, "Could not create the project: " + cerr.Error(), true
		}
		set.ProjectID, set.Label, set.MadeProject = p.ID, p.Label, true
	}
	// Stamp the repo onto the project, which is what makes it resolvable from a checkout afterwards
	// (and what stops the NEXT create_project on another machine from making a second one). Only when
	// we know the id — an adopted-by-remote project is already stamped by definition.
	if set.ProjectID != "" && !stamped {
		if uerr := c.SetProjectRepoURL(set.ProjectID, remote); uerr != nil {
			return nil, fmt.Sprintf("Created the project %q but could not record which repo it belongs to: %s\n\n"+
				"Set the repository on the project in the web app, or this checkout will not resolve to it.",
				set.Label, uerr.Error()), true
		}
	}

	// ---- 2. the thread + the pin -----------------------------------------------------------------
	// The pin is what makes a teammate who pulls land in the SAME thread instead of resolving their
	// own. An existing pin always wins: this tool creates a project, never a second thread.
	if set.Thread == "" {
		th, made, terr := c.ResolveThreadForRepo(remote, set.Label, true)
		if terr != nil || th == nil {
			reason := "the control plane returned no thread"
			if terr != nil {
				reason = terr.Error()
			}
			return nil, fmt.Sprintf("Project %q is set up, but it has no context thread yet (%s). Planning needs one — "+
				"try create_project again, or `ptln thread bind <id>` in this repo.", set.Label, reason), true
		}
		set.Thread, set.MadeThread = th.ID, made
	}
	// VERIFY BEFORE PINNING. `.partyline.json` is checked into the repo and every teammate's session
	// resolves through it, so writing an id we have not READ makes one machine's bad state everyone's.
	// An unreadable id here is not hypothetical: a thread created private (before repo threads were
	// team-visible) resolved for its creator and for nobody else, and the pin sent every later
	// session to a thread it could not open — reported only as "thread not found", with no id and no
	// hint of where it came from, which cost a day of hunting.
	if _, _, verr := c.GetThread(set.Thread); verr != nil {
		return nil, fmt.Sprintf("Project %q is set up, but its context thread %s could not be read back, so it was "+
			"NOT pinned in .partyline.json — pinning an id that does not resolve would hand the same failure to "+
			"everyone who pulls this repo.\n\nReason: %s\n\nRun `ptln doctor` here: it reports this repo's thread "+
			"and project and names the exact command to fix whichever is wrong.", set.Label, set.Thread, verr.Error()), true
	}
	if loadRepoBind(root) != set.Thread {
		if werr := writeRepoPin(root, set.Thread); werr == nil {
			set.WrotePin = true
		}
	}

	// ---- 4. the local registry (step 3, the in-process rebind, belongs to the caller) -------------
	if regErr := registerProjectHere(set.Label, root); regErr != nil {
		set.RegisterErr = regErr.Error()
	} else {
		set.Registered = true
	}

	return set, set.summary(), false
}

// handleCreateProject is the MCP entry point. The one thing it adds to createProjectHere is STEP 3:
// rebinding s.thread IN-PROCESS, so planning_open works in the SAME session. No relaunch is needed —
// cg-mcp holds the thread as a field and the advertised tool list does not change — and needing one
// would defeat the tool, since the user asked for this mid-conversation.
func (s *cgServer) handleCreateProject(enc *json.Encoder, req rpcReq) {
	var pp struct {
		Args struct {
			Label string `json:"label"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &pp)

	set, msg, isErr := createProjectHere(s.c, pp.Args.Label)
	if isErr || set == nil {
		s.toolResult(enc, req.ID, msg, true)
		return
	}
	s.thread = set.Thread
	s.repoLookupDone = true
	s.markConnected()
	// The relay instruction is MCP-only: on the CLI the user is already reading this.
	s.toolResult(enc, req.ID, msg+"\nTell the user what was set up, and say the registration part out loud — "+
		"it is a grant over their machine, not a detail.", false)
}

func projectWithLabel(ps []api.Project, label string) *api.Project {
	for i := range ps {
		if ps[i].Label == label {
			return &ps[i]
		}
	}
	return nil
}

// registerProjectHere adds label → absolute dir to THIS MACHINE's registry — the same registry
// resolveRun matches a run against, which is why promote_work_item refuses without it.
//
// Deliberately NOT registerLocalProject (localrepos.go), which is the web-driven chokepoint and
// differs in two ways that matter here: it REPOINTS an existing label in place, and it prints
// through syncIfEnrolled. Repointing would send every run for that label into a different checkout
// without the person who registered the first one getting a say; printing corrupts the JSON-RPC
// stream this runs on.
func registerProjectHere(label, dir string) error {
	reg := loadDaemonRegistry()
	for i := range reg.Projects {
		if reg.Projects[i].Label != label {
			continue
		}
		if pathsEqual(reg.Projects[i].Path, dir) {
			return nil // already advertised here — nothing to do
		}
		return fmt.Errorf("this machine already advertises %q at %s; it will not be repointed automatically "+
			"(`ptln daemon remove-project %s` first, or pick a different label)", label, reg.Projects[i].Path, label)
	}
	reg.Projects = append(reg.Projects, daemonProject{Label: label, Path: dir, Preset: "spec"})
	if err := saveDaemonRegistry(reg); err != nil {
		return err
	}
	invalidateLocalRepoCache()
	mirrorIfEnrolled() // label only; the path never leaves the machine
	return nil
}

// summary is the ONE message the tool returns. Two things it must always do: say what already
// existed versus what was created (so nobody thinks a duplicate was made), and say plainly what
// registration granted — that agents the team dispatches may build in this directory unattended —
// with how to undo it.
func (p *projectSetup) summary() string {
	var b strings.Builder
	verb := "Adopted the existing project"
	if p.MadeProject {
		verb = "Created project"
	}
	fmt.Fprintf(&b, "%s %q for %s.\n\n", verb, p.Label, p.Path)

	if p.MadeThread {
		fmt.Fprintf(&b, "• Context thread created (%s)", p.Thread)
	} else {
		fmt.Fprintf(&b, "• Using this repo's existing context thread (%s)", p.Thread)
	}
	if p.WrotePin {
		b.WriteString(" and pinned in `.partyline.json` — check that file in, so teammates who pull land in the same thread.\n")
	} else {
		b.WriteString(".\n")
	}

	if p.Registered {
		fmt.Fprintf(&b, "• Registered on THIS MACHINE: %q → %s.\n\n", p.Label, p.Path)
		b.WriteString("  Registration is a grant, not bookkeeping. This directory is now " +
			"available to your team — agents your teammates dispatch can run builds in it UNATTENDED, with no further " +
			"prompt on this machine. Undo it any time with `ptln daemon remove-project " + p.Label + "` — that " +
			"withdraws the directory and nothing else about the project changes.\n\n")
		b.WriteString("Planning works right now in this session — call planning_open, no restart needed.\n")
		return b.String()
	}

	// PARTIAL. Say exactly which half is missing and what still works; a setup that reads as
	// complete and then fails at promote is the worse outcome.
	fmt.Fprintf(&b, "• NOT registered on this machine: %s\n\n", p.RegisterErr)
	b.WriteString("So this is a PARTIAL setup. planning_open works in this session right now, but promote_work_item " +
		"will refuse — nothing here advertises the label, so no machine can be picked to build on. Finish it with " +
		"`ptln daemon add-project " + p.Label + " " + p.Path + "` (that registration also declares this directory " +
		"available for your team's agents to build in unattended).\n")
	return b.String()
}
