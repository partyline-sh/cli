package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
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
	case "activity":
		triggerActivity(args[1:])
	case "log", "events", "fires":
		triggerLog(args[1:])
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
  activity              what every trigger has DONE lately, by outcome
                          ptln trigger activity --days 30 --json
  log <slug>            one trigger's event log — every call, and the run it
                        started. This is the answer to "why did it build that"
                          ptln trigger log deploy-prod --limit 20
  targets               machines you can run on, and the projects each advertises
  create <name>         make one (see FLAGS) — prints the address and the key ONCE
  off <slug>            stop it firing, keep the address reserved
  on <slug>             start it firing again
  rm <slug>             delete it (callers start getting errors)
  set <slug> [flags]    fix one in place — same flags as create, keeping the SAME
                        key so you do not have to touch your CI again
                          ptln trigger set deploy-prod --on failed
                          ptln trigger set deploy-prod --task @brief.md
                          ptln trigger set deploy-prod --cron '0 3 * * *'
                          --on any restores acting on every outcome
                          --cron '' removes the schedule; --schedule pause|resume
                          stops the clock without closing the address

CREATE FLAGS
  --slug <s>            the URL name: /api/v1/t/<s>          (required)
  --project <label>     which project work runs in            (required)
  --machine <name>      which machine                         (required unless only one matches)
  --task <text|@file>   what the agent is asked; {{fields}} come from the caller
                        (optional when --template is given: the persona then runs
                        against the caller's whole payload, fenced as untrusted)
  --on <outcomes>       only act on these, e.g. --on failed   (default: every call)
  --outcome-path <p>    where the result lives in the payload, e.g. data.status
  --success-when <v>    the value that means success, e.g. succeeded
  --gate review|auto    backlog it, or start straight away    (default: review)
  --template <name>     a reusable persona to run (ptln template ls). The template
                        says WHO the agent is; --task says what just happened.
                        One of --task or --template is required
  --preset <p>          spec | chat | build                   (default: spec)
  --cron <expr>         ALSO run on a clock: five-field cron, read in your team's
                        time zone (UTC unless the org sets one). Optional — with no
                        --cron the trigger only fires when something POSTs to it
                          --cron '0 3 * * *'       every day at 3am
                          --cron '*/30 * * * *'    every half hour
                          --cron '0 9 * * mon-fri' weekday mornings
                        A window missed while a machine was down fires ONCE on
                        recovery, and never on top of its own previous run
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
	// Resolve persona ids to NAMES, once, and only when one is actually attached — a uuid in a list
	// is something to go look up, which is the lookup this whole surface exists to remove.
	personas := map[string]string{}
	for _, t := range ts {
		if t.AgentTemplateID != "" {
			if tpls, err := triggerClient().ListTemplates(); err == nil {
				for _, tp := range tpls {
					personas[tp.ID] = tp.Name
				}
			}
			break
		}
	}
	for _, t := range ts {
		state := ""
		if !t.Enabled {
			state = "  (off)"
		}
		if t.AgentTemplateID != "" {
			name := personas[t.AgentTemplateID]
			if name == "" {
				name = t.AgentTemplateID
			}
			state += "  persona: " + name
		}
		acts := "every call"
		if len(t.ActOn) > 0 {
			acts = "on " + strings.Join(t.ActOn, ",")
		}
		// A schedule is the difference between "waits to be called" and "runs at 3am whether you are
		// there or not". It belongs in the one-line summary, not only in --json.
		if t.CronExpr != "" {
			acts = t.CronExpr
			if t.SchedulePaused {
				state += "  (schedule paused)"
			} else if t.NextRunAt != "" {
				state += "  next " + t.NextRunAt
			}
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

// triggerActivity — the CLI half of the dashboard's Triggers panel (#829).
//
// The panel and this command read the SAME endpoint, so they cannot disagree. Text output is the
// per-outcome breakdown, because "fired 40 times" is not the useful number — "fired 40 times and
// started 2 runs" is, and the difference between those two is the entire diagnosis.
func triggerActivity(args []string) {
	days := 0
	if v := flagVal(args, "--days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fatal(fmt.Errorf("--days takes a whole number of days, e.g. --days 30"))
		}
		days = n
	}

	series, window, err := triggerClient().TriggerActivity(days)
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"window_days": window, "triggers": series}, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(series) == 0 {
		fmt.Println("No triggers yet.\n\nMake one:  ptln trigger create \"Deploy triage\" --slug deploy-prod --project <label> --on failed")
		return
	}

	fmt.Printf("Last %d days\n\n", window)
	for _, s := range series {
		state := ""
		if s.Inactive != "" {
			state = "  (" + s.Inactive + ")"
		}
		if s.Total == 0 {
			// The most alarming row on the panel, and the one a summary line would hide.
			fmt.Printf("%-24s never called%s\n", clip(s.Name, 24), state)
			continue
		}
		fmt.Printf("%-24s %d calls%s\n", clip(s.Name, 24), s.Total, state)
		for _, o := range outcomesOf(s) {
			fmt.Printf("    %-16s %d\n", o.name, o.n)
		}
	}
	fmt.Println("\nOne trigger's calls in detail:  ptln trigger log <slug>")
}

type outcomeCount struct {
	name string
	n    int
}

// outcomesOf totals a series by outcome, ordered biggest-first with a stable tiebreak on the name —
// so the same window twice in a row prints the same order.
func outcomesOf(s api.TriggerSeries) []outcomeCount {
	tot := map[string]int{}
	for _, b := range s.Buckets {
		for k, n := range b.Counts {
			tot[k] += n
		}
	}
	out := make([]outcomeCount, 0, len(tot))
	for k, n := range tot {
		out = append(out, outcomeCount{k, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].name < out[j].name
	})
	return out
}

// triggerLog — one trigger's event log, by SLUG.
//
// No id lookup first: the endpoint resolves a slug as readily as a uuid, so the thing you named is
// the thing you type. Rows that started a run print the run id, which is what `ptln run` takes.
func triggerLog(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fatal(fmt.Errorf("ptln trigger log needs a slug:\n  ptln trigger log deploy-prod\n\nSee them all:  ptln trigger ls"))
	}
	slug := args[0]
	rest := args[1:]

	limit := 0
	if v := flagVal(rest, "--limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fatal(fmt.Errorf("--limit takes a whole number of rows, e.g. --limit 20"))
		}
		limit = n
	}

	t, events, truncated, err := triggerClient().TriggerEvents(slug, limit)
	if err != nil {
		fatal(err)
	}
	if hasFlag(rest, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"trigger": t, "events": events, "truncated": truncated}, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("%s  /api/v1/t/%s", t.Name, t.Slug)
	if !t.Enabled {
		fmt.Print("  (off — calls are recorded, nothing runs)")
	}
	fmt.Println()

	if len(events) == 0 {
		fmt.Printf("\nNothing has called this trigger yet.\nPoint something at /api/v1/t/%s with its key in the Authorization header.\n", t.Slug)
		return
	}
	fmt.Println()
	for _, e := range events {
		at := e.At
		if len(at) >= 16 {
			at = strings.Replace(at[:16], "T", " ", 1)
		}
		// What happened, then the run or the reason there isn't one. Never a bare outcome word: the
		// point of the log is the "because".
		detail := e.Skipped
		if e.RunID != "" {
			detail = e.RunID
			if e.RunTitle != "" {
				detail += "  " + clip(e.RunTitle, 48)
			}
		} else if detail == "" {
			detail = "accepted, no run started"
		}
		ref := ""
		if e.Ref != "" {
			ref = "  ref " + e.Ref
		}
		fmt.Printf("%s  %-14s %s%s\n", at, e.Outcome, detail, ref)
	}
	if truncated {
		fmt.Printf("\nOlder calls not shown — raise it with --limit (max 200).\n")
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
		// A CLOCK as well as an address. Empty = webhook-only, exactly as before. The server
		// validates the expression and refuses one it cannot parse, so a typo comes back as an
		// error here rather than as a schedule that quietly never fires.
		CronExpr: flagVal(args, "--cron"),
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

	// --template takes a NAME, resolved here, so nobody has to look up a uuid — same reasoning as
	// --project taking a label.
	if tn := flagVal(args, "--template"); tn != "" {
		in.AgentTemplateID = findTemplate(tn).ID
	}

	t, key, err := triggerClient().CreateTrigger(in)
	if err != nil && t == nil {
		fatal(err)
	}

	// --key-only: the key on stdout, everything else on stderr, so it pipes into `gh secret set`
	// without ever being printed into a transcript or held in a model's context.
	if hasFlag(args, "--key-only") {
		fmt.Fprintf(os.Stderr, "Created %q → %s/api/v1/t/%s\n", t.Name, api.Base(), t.Slug)
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

	fmt.Printf("Created %q\n\n  address  %s/api/v1/t/%s\n", t.Name, api.Base(), t.Slug)
	// Say when it will next run. "Is my cron right?" is the first question anyone has, and the
	// server has already computed the answer — echoing the expression back would not check it.
	if t.CronExpr != "" {
		fmt.Printf("  runs     %s", t.CronExpr)
		if t.NextRunAt != "" {
			fmt.Printf("  (next %s)", t.NextRunAt)
		}
		fmt.Println()
	}
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
	if _, ok := flagPresent(rest, "--template"); ok {
		tn := flagVal(rest, "--template")
		id := ""
		if tn != "" {
			id = findTemplate(tn).ID
		}
		// Empty detaches: back to the inline task alone. Without a way to undo an attachment the
		// only route back would be deleting the trigger, which rotates its key.
		patch.AgentTemplateID = &id
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
	if _, ok := flagPresent(rest, "--cron"); ok {
		v := flagVal(rest, "--cron")
		// Empty REMOVES the schedule — back to webhook-only. Same reasoning as --template "":
		// without a way to undo it the only route back is deleting the trigger, which rotates its
		// key and means going back to your CI.
		patch.CronExpr = &v
	}
	if _, ok := flagPresent(rest, "--schedule"); ok {
		switch flagVal(rest, "--schedule") {
		case "pause", "paused", "off":
			yes := true
			patch.SchedulePaused = &yes
		case "resume", "on":
			no := false
			patch.SchedulePaused = &no
		default:
			fatal(fmt.Errorf("--schedule takes pause or resume (use --cron '' to remove the schedule entirely)"))
		}
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
		patch.ActOn == nil && patch.OutcomePath == nil && patch.SuccessWhen == nil &&
		patch.AgentTemplateID == nil && patch.CronExpr == nil && patch.SchedulePaused == nil {
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
	// Read back, not echoed — for the schedule too. The whole reason this command exists is a
	// setting that was accepted and silently dropped.
	if after.CronExpr != "" {
		when := after.NextRunAt
		if after.SchedulePaused {
			when = "paused"
		} else if when == "" {
			when = "never — that expression matches no real date"
		}
		fmt.Printf("%s — runs %s (next %s)\n", after.Slug, after.CronExpr, when)
	}
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

// flagPresent reports whether a flag appears at all, separately from its value. `--template ""`
// means DETACH and must be distinguishable from not passing --template, which means leave it alone.
func flagPresent(args []string, f string) (string, bool) {
	for i, a := range args {
		if a == f {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(a, f+"=") {
			return strings.TrimPrefix(a, f+"="), true
		}
	}
	return "", false
}
