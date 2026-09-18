package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln template` — reusable agent personas, from a shell.
//
// A template is the WHO: "triage a failed deploy the way a good engineer would". It is authored once
// and pointed at from any number of triggers. Without this command it could be created only through
// the web, so an agent asked to set up deploy triage could build the trigger and not the persona it
// should run — the same half-finished handoff `ptln trigger` was built to remove.
//
// The trigger keeps its own task_template for WHAT JUST HAPPENED, with the {{fields}} from the
// caller's payload. Persona and event compose; neither replaces the other.

func templateCmd(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		templateHelp()
		return
	}
	switch args[0] {
	case "ls", "list":
		templateList(args[1:])
	case "create", "new", "add":
		templateCreate(args[1:])
	case "show":
		templateShow(args[1:])
	case "rm", "delete", "remove":
		templateRm(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ptln template: unknown command %q\n\n", args[0])
		templateHelp()
		os.Exit(2)
	}
}

func templateHelp() {
	fmt.Print(`Usage: ptln template <command>

Reusable agent personas. One template, many triggers — written once instead of
retyped into every webhook.

COMMANDS
  ls                    templates on your team (--json for machine output)
  show <name>           the full instruction a trigger would run (--json too)
  create <name>         author one (see FLAGS)
  rm <name>             remove it — triggers using it fall back to their own
                        inline task, and the command names which ones did

CREATE FLAGS
  --body <text|@file>   WHO the agent is and what it is for. No {{fields}} —
                        those belong on the trigger, which knows what happened.
  --stop-when <text|@file>
                        when it must REFUSE. An unattended agent with no
                        instruction to stop improvises around a missing
                        precondition instead of saying so.
                        (--refuse-when is the same flag.)
  --approve             usable immediately. Without this it is a DRAFT, and a
                        trigger pointing at a draft falls back to its inline
                        task rather than running an unfinished instruction.
  --json                print the created template as JSON

ATTACH ONE
  ptln trigger create … --template "Deploy triage"
  ptln trigger set <slug> --template "Deploy triage"
  ptln trigger set <slug> --template ""      detach, back to the inline task
`)
}

func templateList(args []string) {
	ts, err := triggerClient().ListTemplates()
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(ts, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(ts) == 0 {
		fmt.Println("No agent templates.\n\nAuthor one:  ptln template create \"Deploy triage\" --body @brief.md --approve")
		return
	}
	for _, t := range ts {
		stop := ""
		if strings.TrimSpace(t.StopRule) == "" {
			// Worth surfacing on the list: it is the field that decides whether an unattended agent
			// knows when to give up.
			stop = "  (no stop rule)"
		}
		fmt.Printf("%-28s %-10s%s\n", clip(t.Name, 28), t.Status, stop)
	}
}

func templateShow(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln template show needs a name (see: ptln template ls)"))
	}
	// The list carries no body — resolve the name against it, then read the one template whole.
	// Printing an empty instruction here would be worse than an error: it reads as "this template
	// says nothing", when in fact nobody asked for the part that says something.
	t := findTemplate(args[0])
	full, err := triggerClient().GetTemplate(t.ID)
	if err != nil {
		// The name RESOLVED a moment ago, so a 404 here is not "no such template" — it is a control
		// plane that predates this endpoint. The CLI ships on its own tag; say which of the two is
		// behind rather than making someone doubt the name they just read off `ptln template ls`.
		if strings.Contains(err.Error(), "404") {
			fatal(fmt.Errorf("%q exists, but this control plane has no way to return its body yet — it needs a newer partyline.sh. Until then: ptln template ls", t.Name))
		}
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(full, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("%s  [%s]\n\n%s\n", full.Name, full.Status, strings.TrimRight(full.Body, "\n"))
	if strings.TrimSpace(full.StopRule) != "" {
		fmt.Printf("\nWHEN TO STOP\n%s\n", full.StopRule)
	}
}

func templateRm(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln template rm needs a name (see: ptln template ls)"))
	}
	t := findTemplate(args[0])
	detached, err := triggerClient().DeleteTemplate(t.ID)
	if err != nil {
		// Same reasoning as show: the name resolved, so a 404 is a control plane without the route.
		if strings.Contains(err.Error(), "404") {
			fatal(fmt.Errorf("this control plane cannot remove a template yet — it needs a newer partyline.sh"))
		}
		fatal(err)
	}
	fmt.Printf("Removed %q.\n", t.Name)
	if len(detached) > 0 {
		// Say what it cost. Those triggers still fire — they fall back to their own inline task —
		// so nothing broke, but WHAT THEY ASK CHANGED, and that is not something to find out later.
		fmt.Printf("\n%d trigger(s) lost that persona and now run their inline task alone: %s\n",
			len(detached), strings.Join(detached, ", "))
	}
}

func templateCreate(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fatal(fmt.Errorf("ptln template create needs a name:\n  ptln template create \"Deploy triage\" --body @brief.md --approve"))
	}
	name := args[0]
	rest := args[1:]

	body := flagText(rest, "--body")
	if strings.TrimSpace(body) == "" {
		fatal(fmt.Errorf("--body is required — it is the instruction the agent runs.\n  ptln template create %q --body @brief.md", name))
	}
	// --refuse-when is the same field under the name the docs and the API reviewers reach for first.
	// One flag with two spellings beats a second field nobody sets.
	stop := flagText(rest, "--stop-when")
	if strings.TrimSpace(stop) == "" {
		stop = flagText(rest, "--refuse-when")
	}

	status := "draft"
	if hasFlag(rest, "--approve") {
		status = "approved"
	}

	id, err := triggerClient().CreateTemplate(name, body, stop, status)
	if err != nil {
		fatal(err)
	}
	if hasFlag(rest, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"id": id, "name": name, "status": status}, "", "  ")
		fmt.Println(string(b))
		return
	}
	// READ IT BACK. `ptln trigger set` learned this the hard way: the whole act_on bug was a setting
	// accepted and silently discarded, and a confirmation that repeats the request would have said
	// "approved" just as cheerfully. It just happened again here — --approve was dropped server-side
	// and this message claimed success.
	if after := findTemplateOrNil(name); after != nil {
		status = after.Status
	}

	if status == "draft" {
		// Say it plainly. A draft attached to a trigger is silently ignored at fire time, which is
		// the safe behaviour and the confusing one if nobody mentioned it.
		fmt.Printf("Created %q as a DRAFT.\n\nA trigger pointing at a draft falls back to its own inline task — nothing will run this yet.\nRe-create with --approve when it is ready.\n", name)
		return
	}
	fmt.Printf("Created %q and approved it.\n\nAttach it:  ptln trigger set <slug> --template %q\n", name, name)
}

// readTextArg takes a flag's value as literal text, or reads it from @file. A persona is paragraphs;
// shell-quoting paragraphs is how they arrive mangled.
func flagText(args []string, flag string) string {
	v := flagVal(args, flag)
	if !strings.HasPrefix(v, "@") {
		return v
	}
	b, err := os.ReadFile(strings.TrimPrefix(v, "@"))
	if err != nil {
		fatal(fmt.Errorf("%s %s: %w", flag, v, err))
	}
	return string(b)
}

// findTemplate resolves a NAME to a template, naming what IS there when it cannot — nobody should
// have to look up a uuid to attach a persona.
func findTemplate(name string) api.AgentTemplate {
	ts, err := triggerClient().ListTemplates()
	if err != nil {
		fatal(err)
	}
	for _, t := range ts {
		if strings.EqualFold(t.Name, name) || t.ID == name {
			return t
		}
	}
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		fatal(fmt.Errorf("no agent templates yet — author one with: ptln template create"))
	}
	fatal(fmt.Errorf("no template %q. Available: %s", name, strings.Join(names, ", ")))
	return api.AgentTemplate{}
}

// findTemplateOrNil resolves a name without exiting when it is absent — for reading back a write,
// where "I could not confirm it" must not be reported as a failure of the write itself.
func findTemplateOrNil(name string) *api.AgentTemplate {
	ts, err := triggerClient().ListTemplates()
	if err != nil {
		return nil
	}
	for i, t := range ts {
		if strings.EqualFold(t.Name, name) {
			return &ts[i]
		}
	}
	return nil
}
