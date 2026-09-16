package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln project doc` and `ptln project env` — the two project settings that change what a run does.
//
// The document is the project globals: the brief injected into every worker's CLAUDE.md before it
// starts. It is the single highest-leverage setting in the product, and it was web-only — so an
// agent could be told to "set up this project" and then had no way to write down what it learned.
//
// Both take a project LABEL, not a uuid. An agent should never have to run one command to look up
// an id and paste it into another.

// resolveProject turns a label into a project id, and names what IS available when it cannot.
func resolveProject(c *api.Client, label string) (*api.Project, error) {
	ps, err := c.ListProjects()
	if err != nil {
		return nil, err
	}
	for i, p := range ps {
		if p.Label == label || p.ID == label {
			return &ps[i], nil
		}
	}
	labels := make([]string, 0, len(ps))
	for _, p := range ps {
		labels = append(labels, p.Label)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("no projects yet — make one: ptln project new \"<label>\"")
	}
	return nil, fmt.Errorf("no project called %q. Available: %s", label, strings.Join(labels, ", "))
}

// ── ptln project doc ─────────────────────────────────────────────────────────────────────────────

func projectDoc(c *api.Client, args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln project doc <label>            print the project document\n" +
			"       ptln project doc <label> --set @FILE  replace it (- for stdin)"))
	}
	p, err := resolveProject(c, args[0])
	if err != nil {
		fatal(err)
	}
	rest := args[1:]

	set := flagVal(rest, "--set")
	if set == "" {
		doc, err := c.ProjectDocument(p.ID)
		if err != nil {
			fatal(err)
		}
		if hasFlag(rest, "--json") {
			b, _ := json.MarshalIndent(doc, "", "  ")
			fmt.Println(string(b))
			return
		}
		if strings.TrimSpace(doc.Body) == "" {
			fmt.Printf("%s has no project document yet.\n\nWrite one:  ptln project doc %s --set @BRIEF.md\nIt is injected into every run on this project.\n", p.Label, p.Label)
			return
		}
		fmt.Print(doc.Body)
		if !strings.HasSuffix(doc.Body, "\n") {
			fmt.Println()
		}
		return
	}

	// @file or - for stdin, because a project brief is pages long and shell-quoting pages is how
	// they arrive mangled.
	body, err := readTextArg(set)
	if err != nil {
		fatal(err)
	}
	if err := c.SetProjectDocument(p.ID, body); err != nil {
		fatal(err)
	}
	fmt.Printf("Updated the %s project document (%d characters). Every new run on this project gets it.\n", p.Label, len(body))
}

// readTextArg reads @file, - (stdin), or the literal string.
func readTextArg(v string) (string, error) {
	switch {
	case v == "-":
		b, err := os.ReadFile("/dev/stdin")
		return string(b), err
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(strings.TrimPrefix(v, "@"))
		if err != nil {
			return "", fmt.Errorf("--set %s: %w", v, err)
		}
		return string(b), nil
	default:
		return v, nil
	}
}

// ── ptln project env ─────────────────────────────────────────────────────────────────────────────

func projectEnv(c *api.Client, args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln project env <label>                       the deploy chain\n" +
			"       ptln project env <label> --set staging=develop,prod=main"))
	}
	p, err := resolveProject(c, args[0])
	if err != nil {
		fatal(err)
	}
	rest := args[1:]

	if set := flagVal(rest, "--set"); set != "" {
		var envs []api.Environment
		for _, pair := range strings.Split(set, ",") {
			name, branch, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(branch) == "" {
				fatal(fmt.Errorf("--set takes name=branch pairs, e.g. --set staging=develop,prod=main (got %q)", pair))
			}
			envs = append(envs, api.Environment{Name: strings.TrimSpace(name), Branch: strings.TrimSpace(branch)})
		}
		// The order given IS the promotion order — the whole chain is replaced, because "add one"
		// has no meaning without saying where it goes.
		if err := c.SetEnvironments(p.ID, envs); err != nil {
			fatal(err)
		}
		parts := make([]string, 0, len(envs))
		for _, e := range envs {
			parts = append(parts, e.Name+" ("+e.Branch+")")
		}
		fmt.Printf("%s deploys: %s\n", p.Label, strings.Join(parts, " → "))
		return
	}

	envs, err := c.Environments(p.ID)
	if err != nil {
		fatal(err)
	}
	if hasFlag(rest, "--json") {
		b, _ := json.MarshalIndent(envs, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(envs) == 0 {
		fmt.Printf("%s has no environments.\n\nSet the chain:  ptln project env %s --set staging=develop,prod=main\n", p.Label, p.Label)
		return
	}
	for _, e := range envs {
		fmt.Printf("%-16s %s\n", e.Name, e.Branch)
	}
}
