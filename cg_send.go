package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// `send_to_partyline` — the verb people actually say (#840, epic #836).
//
// The pipe already existed: propose_work_item files one node, plan_file_tree files a whole
// decomposition, and promote-tree turns a container's leaves into a serialized chain. What was
// missing was a door reachable from the sentence someone actually utters — "this feature is great,
// send it to partyline" — and, more importantly, a filing path that cannot quietly produce
// something unbuildable.
//
// The old failure was specific and invisible: propose_work_item passed `readiness` through
// untouched, so a model that omitted it filed a readiness-0 item, which sat in the backlog looking
// real and refused to Start. It read as "partyline won't run tasks it didn't plan." Everything here
// funnels through the server's derived gate instead, so the outcome is either a runnable item or a
// question — never a decoration.

// sendProjectLabel guesses which project this session is filing for.
//
// The session is almost always running INSIDE the repo the work is about, so the answer is usually
// sitting right there in the daemon registry. Guessing it is worth real effort: `target` is one of
// the four gate dimensions, so a wrong or absent label is the difference between filing and a round
// trip. An explicit argument always wins — this only fills the silence.
//
// Matches the DEEPEST registered project containing the cwd, so a repo registered inside another
// repo resolves to the inner one rather than whichever happened to be listed first.
func sendProjectLabel(explicit string) string {
	if l := strings.TrimSpace(explicit); l != "" {
		return l
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	cwd = realPath(cwd)
	best, bestLen := "", -1
	for _, p := range loadDaemonRegistry().Projects {
		dir := realPath(p.Path)
		if dir == "" || dir == "." {
			continue
		}
		// The separator matters: a plain string prefix would match /dev/app against a session in
		// /dev/app-v2 and file the work against the wrong project.
		if cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator)) {
			if len(dir) > bestLen {
				best, bestLen = p.Label, len(dir)
			}
		}
	}
	return best
}

// realPath resolves symlinks before comparing two paths.
//
// Without it the match silently fails wherever a path crosses a symlink: os.Getwd() hands back the
// RESOLVED physical path, while the registry holds whatever string was typed at `add-project`. On
// macOS /tmp and /var are symlinks, so the two never line up — and the failure is invisible, because
// the label just comes back empty and the item gets held back for a missing target with no hint
// that a path comparison was the reason.
func realPath(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" || p == "." {
		return p
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return p // not on disk (a stale registry entry) — compare what we were given
}

// cgSendToolDef is the tool description — the contract, in the one place the model always reads it.
//
// It carries the task shape rather than assuming a skill was loaded: a skill is advisory and may not
// be in context, while a tool description is present at the moment of the call, costs nothing, and
// is the cheapest possible place to teach the shape.
var cgSendToolDef = map[string]any{
	"name": "send_to_partyline",
	"description": "Send work to the partyline backlog so it can be BUILT autonomously — the tool for when a human says " +
		"\"send this to partyline\", \"put this in the backlog\", or \"let's build this\". " +
		"Takes ONE well-specified task, or a whole decomposition (epic ▸ feature ▸ task) when the work is bigger than a single change — " +
		"a decomposition is promoted as a chain, so its tasks build one after another, each landing its own reviewable branch. " +
		"\n\nIT MAY COME BACK WITH QUESTIONS INSTEAD OF FILING. That is normal and is the tool working, not failing. " +
		"An underspecified task produces a confident, wrong diff, so the server checks four things it can verify: the project exists, " +
		"there is a command that proves the work is done, the task fits one session, and there is a spec beyond the title. " +
		"If something is missing you get the exact questions — put them to the human AS WRITTEN and call again with the answers. " +
		"Do NOT invent answers and do NOT pad the description: the checks are structural, so more prose changes nothing. " +
		"\n\nUse `preview: true` to check a draft before committing to it. " + cgCrankTaskShape,
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "a concise imperative title — what this change does."},
			"document": map[string]any{
				"type":        "string",
				"description": "the spec: which files/behaviour are involved, the approach, the edge cases. This is what the building agent works from, so be exact — name real paths and symbols rather than describing them in general terms.",
			},
			"acceptance_criteria": map[string]any{
				"type":        "array",
				"description": "the definition of done. AT LEAST ONE must have verify = \"executable check\" and name a real command (a test, a build, a typecheck) — that is what turns \"looks right\" into \"is right\", and without it the item will be held back.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text":   map[string]any{"type": "string", "description": "for an executable check, the COMMAND to run, e.g. `go test ./...` or `npx vitest run src/login`."},
						"verify": map[string]any{"type": "string", "enum": []string{"executable check", "adversarial review", "behavior review"}},
					},
				},
			},
			"project_label": map[string]any{"type": "string", "description": "which partyline project builds this. Usually inferred from the current directory — pass it only to override, or if the guess was wrong."},
			"tree": map[string]any{
				"type":        "object",
				"description": "for work bigger than one task: {kind, title, document?, acceptance_criteria?, children?[...]} — epic ▸ feature ▸ task, 3 levels max. Every TASK leaf needs its own executable check. Use INSTEAD of title/document, not alongside.",
			},
			"preview":     map[string]any{"type": "boolean", "description": "score it without filing, to check a draft."},
			"source_tool": map[string]any{"type": "string", "description": "if this came from a tracker, which one ('linear', 'jira', 'github') — recorded so the board can get back to the original."},
			"source_url":  map[string]any{"type": "string", "description": "link to the original ticket, if there is one."},
		},
	},
}

// cgSendResultText is the success message. It names what happens NEXT, because "recorded" leaves the
// model with nothing useful to tell the human — and the honest answer ("it is in the backlog and
// will not run until you start it") is also the reassuring one.
func cgSendResultText(what, id string, count int) string {
	if count > 1 {
		return "Filed " + what + " to the partyline backlog (" + strconv.Itoa(count) + " items, root " + id + ").\n\n" +
			"It is in the backlog and will not run until someone starts it. Promoting the container runs its tasks as a chain — " +
			"one after another, each landing its own branch to review."
	}
	return "Filed " + what + " to the partyline backlog (id " + id + ").\n\n" +
		"It is ready to build and will not run until someone starts it, from the board or with the run controls."
}

// sanitizeForDoc strips the characters that would let third-party text escape the markdown it is
// embedded in. The Go mirror of the web's sanitizeValue (#841) — a tracker URL or tool name reaches
// us from the same untrusted place a ticket body does, and it is rendered on the board and read by
// a building agent.
func sanitizeForDoc(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r < 0x20 || r == 0x7f { // control characters
			continue
		}
		b.WriteRune(r)
	}
	return strings.ReplaceAll(strings.TrimSpace(b.String()), "```", "ˋˋˋ")
}
