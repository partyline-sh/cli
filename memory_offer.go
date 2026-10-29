package main

// FIRST USE IN A REPO: the session offers to set memory up, so nobody has to know the commands.
//
// A repo with no memory/ used to get nothing: no brief, no capture, and no hint that `ptln memory
// build` exists. The only way in was a person typing the setup out, and the person this is for is
// already in an agent session that can run every one of those commands itself. So the per-turn
// hook tells the agent, once, to offer it; a "yes" is all the person types.
//
// ONCE PER REPO, not per session: a person who ignores or declines the offer in one session must
// not get it again in the next, and a repo with a dozen worktrees is still one repo. The marker is
// machine-local on purpose — declining is one person's choice, and writing it into the repo would
// decide for every teammate who clones it.
//
// Capture deliberately does NOT run in a repo without memory/ (memoryTarget): it would create the
// directory in every repo a person ever opens a session in, including the ones they said no for.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// memoryTarget is where a hook running in dir reads and writes facts: the repo itself when it
// already has memory/, else a legacy side-repo project. ok=false means there is nowhere yet, which
// for a git repo is the case memoryOffer handles.
func memoryTarget(dir string) (wsDir, label, repoName string, ok bool) {
	if root, isRepo := repoMemory(dir); isRepo && hasRepoMemory(root) {
		return root, filepath.Base(root), "", true
	}
	if ws, inProject := projectForDir(dir); inProject {
		return ws.Dir, ws.Label, ws.repoNameFor(dir), true
	}
	return "", "", "", false
}

// memoryOfferText is what the agent is told. It says plainly that the offer is optional and comes
// after the person's own request, because a hook that takes over the first turn of a session is
// worse than no hook.
const memoryOfferText = `partyline: this repository has no repository memory (no memory/ directory). Repository memory
stores durable facts about the codebase in memory/, one markdown file each, and summarises them
into AGENTS.md / CLAUDE.md / GEMINI.md, which every session reads at start.

Finish the user's current request first. Then ask once, in one sentence, whether to set it up.
If they decline or do not answer, do not ask again. If they agree:
1. Run ` + "`ptln memory build --apply`" + `. It records build, test and CI facts it can cite, a few
   open questions, and an overview of the project written from the last commit.
2. Read the overview against the code. Correct anything wrong or missing with
   ` + "`ptln memory edit <id> \"...\"`" + `, keeping it a complete description of the project. A code
   comment or README line is a claim, not a fact: check it against the code or config, or say
   where it came from ("the README says ...").
3. Answer the open questions (kind ` + "`question`" + ` in ` + "`ptln memory ls`" + `) that you can verify from
   the code, git history and docs: record the answer with ` + "`ptln memory add <kind> \"...\"`" + ` and
   remove the question with ` + "`ptln memory rm <id>`" + `. Questions only a person can answer (who uses
   it, who owns it, what must never change): draft an answer from the README and docs, show the
   drafts to the user, and record only what they confirm or correct. Leave the rest.
4. Add only the facts a new engineer would need and could not get from the overview: a few rules,
   interface contracts, decisions, and traps. A decision states what was chosen, why, and what it
   costs. Each fact must make sense on its own: name the component, say why it matters, and name
   the evidence (file, commit, ticket). Keep what you checked apart from what you infer; if you
   cannot find out, record a question instead of a guess. Do not copy status or progress notes.
5. Do not commit. Show ` + "`ptln memory ls`" + ` and ` + "`git status --short memory/ AGENTS.md`" + `.
`

// memoryOffer returns memoryOfferText the first time it is asked about a repo on this machine,
// and "" every time after, for a repo that already has memory, and outside a git repo.
func memoryOffer(dir string) string {
	root, ok := repoMemory(dir)
	if !ok || hasRepoMemory(root) {
		return ""
	}
	marker := memoryOfferMarker(root)
	// Mark BEFORE offering, and only offer if THIS call created the marker. O_EXCL makes that
	// atomic, so two sessions whose first turns land together (worktrees) cannot both offer. A
	// marker that cannot be written means no offer at all, which beats one repeated every turn.
	if os.MkdirAll(filepath.Dir(marker), 0o700) != nil {
		return ""
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ""
	}
	_, _ = f.WriteString(root + "\n")
	_ = f.Close()
	return memoryOfferText
}

// memoryOfferMarker names the repo by its git COMMON dir, so every worktree of one repository
// shares one marker.
func memoryOfferMarker(root string) string {
	key := root
	if out, err := gitMem(root, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil && strings.TrimSpace(out) != "" {
		key = strings.TrimSpace(out)
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(stateDir(), "memory-offered", hex.EncodeToString(sum[:8]))
}
