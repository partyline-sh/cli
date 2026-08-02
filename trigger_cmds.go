package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln trigger` — configuring partyline from a shell, so an agent can do the setup.
//
// THE POINT. Everything a trigger needs was previously only reachable by a human clicking through
// Settings. That made "ask your LLM to set up a deploy triage bot" impossible: the agent could
// write the CI step but not create the thing the CI step talks to, so every setup stalled halfway
// with a list of instructions for a person.
//
// `ptln login` already put a token on this machine. So the CLI can act as the user — no browser, no
// device flow, no clicking. An agent that has your shell can now finish the job it could previously
// only describe.
//
// THIS GRANTS NO NEW POWER, which is the important part. An agent with your shell could already
// curl the same API with the same token; it just had to improvise the HTTP from a doc it read.
// Naming the operations makes them validated, consistently shaped, and visible in `ptln trigger ls`
// — strictly safer than the alternative, which is happening anyway.
//
// THE KEY NEVER NEEDS TO PASS THROUGH THE AGENT. `--key-only` puts the key on stdout and everything
// else on stderr, so it pipes straight into whatever consumes it:
//
//	ptln trigger create … --key-only | gh secret set PARTYLINE_DEPLOY_KEY
//
// The secret goes process → process. It is never printed into a transcript, never held in a model's
// context, and never sits on a clipboard — which makes this path better than a human copying it out
// of the web UI by hand, not merely equivalent.

func triggerCmd(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		triggerHelp()
		return
	}
	switch args[0] {
	case "ls", "list":
		triggerList(args[1:])
	case "create", "new", "add":
		triggerCreate(args[1:])
	case "rm", "delete":
		triggerSetState(args[1:], "delete")
	case "off", "disable":
		triggerSetState(args[1:], "off")
	case "on", "enable":
		triggerSetState(args[1:], "on")
	case "set", "edit", "update":
		triggerSet(args[1:])
	case "targets":
		triggerTargets(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ptln trigger: unknown command %q\n\n", args[0])
		triggerHelp()
		os.Exit(2)
	}
}

func triggerHelp() {
	fmt.Print(`Usage: ptln trigger <command>

Inbound entry points: an address other software POSTs to, which starts work here.
You choose the project, the machine and what gets asked; the caller supplies only
data about what happened. See https://partyline.sh/docs/deploys

COMMANDS
  ls                    triggers on your team (--json for machine output)
  targets               machines you can run on, and the projects each advertises
  create <name>         make one (see FLAGS) — prints the address and the key ONCE
  off <slug>            stop it firing, keep the address reserved
  on <slug>             start it firing again
  rm <slug>             delete it (callers start getting errors)
  set <slug> [flags]    fix one in place — same flags as create, keeping the SAME
                        key so you do not have to touch your CI again
                          ptln trigger set deploy-prod --on failed
                          ptln trigger set deploy-prod --task @brief.md
                          --on any restores acting on every outcome

CREATE FLAGS
  --slug <s>            the URL name: /api/v1/t/<s>          (required)
  --project <label>     which project work runs in            (required)
  --machine <name>      which machine                         (required unless only one matches)
  --task <text|@file>   what the agent is asked; {{fields}} come from the caller
  --on <outcomes>       only act on these, e.g. --on failed   (default: every call)
  --outcome-path <p>    where the result lives in the payload, e.g. data.status
  --success-when <v>    the value that means success, e.g. succeeded
  --gate review|auto    backlog it, or start straight away    (default: review)
  --preset <p>          spec | chat | build                   (default: spec)
  --key-only            print ONLY the key on stdout, so it can be piped:
                          ptln trigger create … --key-only | gh secret set PARTYLINE_KEY
  --json                print the created trigger as JSON

EXAMPLE — a deploy triage bot, end to end
  ptln trigger create "Deploy triage" --slug deploy-prod \
    --project my-app --machine mini --on failed \
    --task @investigator.md --key-only | gh secret set PARTYLINE_DEPLOY_KEY
`)
}

func triggerClient() *api.Client {
	c := api.New()
	if c == nil {
		fatal(fmt.Errorf("not logged in on this machine — run: ptln login"))
	}
	return c
}

func triggerList(args []string) {
	asJSON := hasFlag(args, "--json")
	ts, err := triggerClient().ListTriggers()
	if err != nil {
		fatal(err)
	}
	if asJSON {
		b, _ := json.MarshalIndent(ts, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(ts) == 0 {
		// An empty list should teach the next step rather than just being empty.
		fmt.Println("No triggers yet.\n\nMake one:  ptln trigger create \"Deploy triage\" --slug deploy-prod --project <label> --on failed")
		return
	}
	for _, t := range ts {
		state := ""
		if !t.Enabled {
			state = "  (off)"
		}
		acts := "every call"
		if len(t.ActOn) > 0 {
			acts = "on " + strings.Join(t.ActOn, ",")
		}
		fmt.Printf("%-24s /api/v1/t/%-22s %-14s fired %d%s\n", clip(t.Name, 24), t.Slug, acts, t.FireCount, state)
	}
}

func triggerTargets(args []string) {
	ts, err := triggerClient().ListTargets()
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(ts, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(ts) == 0 {
		fmt.Println("No machines. Start the daemon on a machine and register a project:\n  ptln daemon add-project <dir> --label <name>")
		return
	}
	for _, t := range ts {
		st := "online"
		if !t.Online {
			st = "offline"
		}
		fmt.Printf("%-22s %-8s %s\n", t.DeviceLabel, st, strings.Join(t.Projects, ", "))
	}
}

func triggerCreate(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fatal(fmt.Errorf("ptln trigger create needs a name: ptln trigger create \"Deploy triage\" --slug deploy-prod --project <label>"))
	}
	in := api.NewTrigger{
		Name:         args[0],
		Slug:         flagVal(args, "--slug"),
		ProjectLabel: flagVal(args, "--project"),
		TaskTemplate: flagVal(args, "--task"),
		Gate:         flagVal(args, "--gate"),
		Preset:       flagVal(args, "--preset"),
		OutcomePath:  flagVal(args, "--outcome-path"),
		SuccessWhen:  flagVal(args, "--success-when"),
	}
	if on := flagVal(args, "--on"); on != "" {
		for _, o := range strings.Split(on, ",") {
			if o = strings.TrimSpace(o); o != "" {
				in.ActOn = append(in.ActOn, o)
			}
		}
	}
	// @file for the task, because an investigator brief is a paragraph and shell-quoting a
	// paragraph is how it arrives mangled.
	if strings.HasPrefix(in.TaskTemplate, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(in.TaskTemplate, "@"))
		if err != nil {
			fatal(fmt.Errorf("--task %s: %w", in.TaskTemplate, err))
		}
		in.TaskTemplate = string(b)
	}
	if in.Slug == "" || in.ProjectLabel == "" {
		fatal(fmt.Errorf("--slug and --project are required (see: ptln trigger help)"))
	}

	// Resolve the machine HERE rather than making the caller find a uuid. An agent should not have
	// to run one command to look up an id and paste it into another.
	daemonID, err := resolveMachine(flagVal(args, "--machine"), in.ProjectLabel)
	if err != nil {
		fatal(err)
	}
	in.DaemonID = daemonID

	t, key, err := triggerClient().CreateTrigger(in)
	if err != nil && t == nil {
		fatal(err)
	}

	// --key-only: the key on stdout, everything else on stderr, so it pipes into `gh secret set`
	// without ever being printed into a transcript or held in a model's context.
	if hasFlag(args, "--key-only") {
		fmt.Fprintf(os.Stderr, "Created %q → https://partyline.sh/api/v1/t/%s\n", t.Name, t.Slug)
		if key == "" {
			fatal(fmt.Errorf("no key was issued — make one on Settings → Integrations"))
		}
		fmt.Println(key)
		return
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"trigger": t, "key": key}, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("Created %q\n\n  address  https://partyline.sh/api/v1/t/%s\n", t.Name, t.Slug)
	if key != "" {
		fmt.Printf("  key      %s\n\nThe key is shown once. Store it now — for CI:\n  gh secret set PARTYLINE_DEPLOY_KEY --body '%s'\n", key, key)
		fmt.Printf("\nNext time, skip the copy/paste entirely:\n  ptln trigger create … --key-only | gh secret set PARTYLINE_DEPLOY_KEY\n")
	} else if err != nil {
		fmt.Printf("\n%v\n", err)
	}
}

// resolveMachine turns a machine name into a daemon id, and gives a USEFUL error when it cannot.
//
// With one obvious answer it picks it — a team with a single machine should not have to name it.
// With several it lists them, because "ambiguous" without the options is a dead end.
func resolveMachine(name, project string) (string, error) {
	ts, err := triggerClient().ListTargets()
	if err != nil {
		return "", err
	}
	var match []api.Target
	for _, t := range ts {
		if name != "" && !strings.EqualFold(t.DeviceLabel, name) {
			continue
		}
		if project != "" && !advertises(t.Projects, project) {
			continue
		}
		match = append(match, t)
	}
	switch len(match) {
	case 1:
		return match[0].DaemonID, nil
	case 0:
		if len(ts) == 0 {
			return "", fmt.Errorf("no machines are registered — start the daemon and add the project:\n  ptln daemon add-project <dir> --label %s", project)
		}
		var lines []string
		for _, t := range ts {
			lines = append(lines, fmt.Sprintf("  %-20s %s", t.DeviceLabel, strings.Join(t.Projects, ", ")))
		}
		return "", fmt.Errorf("no machine advertises project %q. Available:\n%s", project, strings.Join(lines, "\n"))
	default:
		var names []string
		for _, t := range match {
			names = append(names, t.DeviceLabel)
		}
		return "", fmt.Errorf("several machines run %q — pick one with --machine: %s", project, strings.Join(names, ", "))
	}
}

func triggerSetState(args []string, action string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln trigger %s needs a slug (see: ptln trigger ls)", action))
	}
	slug := args[0]
	ts, err := triggerClient().ListTriggers()
	if err != nil {
		fatal(err)
	}
	var found *api.Trigger
	for i := range ts {
		if ts[i].Slug == slug || ts[i].ID == slug {
			found = &ts[i]
			break
		}
	}
	if found == nil {
		fatal(fmt.Errorf("no trigger %q (see: ptln trigger ls)", slug))
	}
	switch action {
	case "delete":
		if err := triggerClient().DeleteTrigger(found.ID); err != nil {
			fatal(err)
		}
		fmt.Printf("Deleted %q. Anything still calling that address will now get an error.\n", found.Name)
	case "off":
		if err := triggerClient().SetTriggerEnabled(found.ID, false); err != nil {
			fatal(err)
		}
		fmt.Printf("%q is off. The address stays reserved.\n", found.Name)
	case "on":
		if err := triggerClient().SetTriggerEnabled(found.ID, true); err != nil {
			fatal(err)
		}
		fmt.Printf("%q is on.\n", found.Name)
	}
}

// ---- small helpers ----

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func flagVal(args []string, f string) string {
	for i, a := range args {
		if a == f && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, f+"=") {
			return strings.TrimPrefix(a, f+"=")
		}
	}
	return ""
}

// clip shortens a name for the list column. Named apart from llms.go's trunc, which is the mux's.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// advertises reports whether a machine offers this project label. Named apart from work_test.go's
// hasStr, which is a test helper for a different question.
func advertises(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

// triggerSet corrects a trigger IN PLACE (#854).
//
// Before this, `enabled` was the only thing that could be changed — so a trigger with the wrong
// instruction, the wrong gate, or (the case that started this) no `--on failed` had to be deleted
// and recreated. Recreating mints a NEW KEY, which means going back to your CI to replace the
// secret. A one-word typo cost a credential rotation, and the cheapest way out was usually to
// leave the mistake in place.
//
// Only the flags actually passed are sent, so fixing one thing never blanks another.
func triggerSet(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fatal(fmt.Errorf("ptln trigger set needs a slug:\n  ptln trigger set deploy-prod --on failed"))
	}
	slug := args[0]
	rest := args[1:]

	t := findTrigger(slug)
	var patch api.TriggerPatch

	if v := flagVal(rest, "--name"); v != "" {
		patch.Name = &v
	}
	if v := flagVal(rest, "--gate"); v != "" {
		if v != "review" && v != "auto" {
			fatal(fmt.Errorf("--gate takes review or auto"))
		}
		patch.Gate = &v
	}
	if v := flagVal(rest, "--outcome-path"); v != "" {
		patch.OutcomePath = &v
	}
	if v := flagVal(rest, "--success-when"); v != "" {
		patch.SuccessWhen = &v
	}
	if v := flagVal(rest, "--task"); v != "" {
		if strings.HasPrefix(v, "@") {
			b, err := os.ReadFile(strings.TrimPrefix(v, "@"))
			if err != nil {
				fatal(fmt.Errorf("--task %s: %w", v, err))
			}
			v = string(b)
		}
		patch.TaskTemplate = &v
	}
	if v := flagVal(rest, "--on"); v != "" {
		var on []string
		// "any" / "all" is how you go BACK to acting on every outcome — without it there would be
		// no way to undo a narrowing except by deleting the trigger, which is the whole problem
		// this command exists to solve.
		if v != "any" && v != "all" {
			for _, o := range strings.Split(v, ",") {
				if o = strings.TrimSpace(o); o != "" {
					on = append(on, o)
				}
			}
		}
		patch.ActOn = &on
	}

	if patch.Name == nil && patch.Gate == nil && patch.TaskTemplate == nil &&
		patch.ActOn == nil && patch.OutcomePath == nil && patch.SuccessWhen == nil {
		fatal(fmt.Errorf("nothing to change. Try:\n  ptln trigger set %s --on failed\n  ptln trigger set %s --task @brief.md\n  ptln trigger set %s --gate review", slug, slug, slug))
	}
	if err := triggerClient().UpdateTrigger(t.ID, patch); err != nil {
		fatal(err)
	}
	// Read it BACK rather than reporting what we asked for. This whole bug was a setting that was
	// accepted and silently dropped; a confirmation that only echoes the request would have said
	// "on failed" just as cheerfully.
	after := findTrigger(slug)
	acts := "every call"
	if len(after.ActOn) > 0 {
		acts = "on " + strings.Join(after.ActOn, ",")
	}
	fmt.Printf("%s — acts %s, gate %s, %d character instruction\n", after.Slug, acts, after.Gate, len(after.TaskTemplate))
}

// findTrigger resolves a slug (or id) to a trigger, naming what IS there when it cannot.
func findTrigger(slug string) api.Trigger {
	ts, err := triggerClient().ListTriggers()
	if err != nil {
		fatal(err)
	}
	for _, t := range ts {
		if t.Slug == slug || t.ID == slug {
			return t
		}
	}
	slugs := make([]string, 0, len(ts))
	for _, t := range ts {
		slugs = append(slugs, t.Slug)
	}
	if len(slugs) == 0 {
		fatal(fmt.Errorf("no triggers yet — make one with: ptln trigger create"))
	}
	fatal(fmt.Errorf("no trigger %q. Available: %s", slug, strings.Join(slugs, ", ")))
	return api.Trigger{}
}
