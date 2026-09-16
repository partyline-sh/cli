package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln team set` — the team-wide settings, from a shell.
//
// These are the settings that decide how the whole fleet behaves: whether finished work waits for a
// human, which engine and model runs by default, and how many runs may go at once. They were
// web-only, which meant an agent could set up a project but not the policy the project runs under.
//
// The review gate is the load-bearing one. `--review off` removes the human checkpoint before work
// merges, so it prints what it did in plain words rather than a terse ok — a setting that changes
// who approves your code should never be silently confirmed.

func teamSet(c *api.Client, args []string) {
	patch := map[string]any{}

	if v := flagVal(args, "--name"); v != "" {
		patch["name"] = v
	}
	if v := flagVal(args, "--engine"); v != "" {
		patch["default_engine"] = normalizeOff(v)
	}
	if v := flagVal(args, "--model"); v != "" {
		patch["default_model"] = normalizeOff(v)
	}
	if v := flagVal(args, "--git-provider"); v != "" {
		patch["git_provider"] = v
	}
	if v := flagVal(args, "--review"); v != "" {
		on, err := onOff(v)
		if err != nil {
			fatal(fmt.Errorf("--review takes on or off"))
		}
		patch["require_review"] = on
	}
	for flag, field := range map[string]string{
		"--max-runs":         "max_concurrent_runs",
		"--max-runs-machine": "max_runs_per_machine",
	} {
		v := flagVal(args, flag)
		if v == "" {
			continue
		}
		if v == "none" || v == "unlimited" {
			patch[field] = nil
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fatal(fmt.Errorf("%s takes a whole number ≥ 1, or 'none' for unlimited", flag))
		}
		patch[field] = n
	}

	if len(patch) == 0 {
		teamSetHelp()
		return
	}

	slug := myOrgSlug(c)
	if err := c.UpdateOrgSettings(slug, patch); err != nil {
		fatal(err)
	}

	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "require_review":
			// Spelled out, because this one changes who approves code reaching your main branch.
			if patch[k] == true {
				fmt.Println("Review gate ON — finished work waits in Review until a human accepts it.")
			} else {
				fmt.Println("Review gate OFF — finished work can merge without a human accepting it first.")
			}
		default:
			v := patch[k]
			if v == nil {
				v = "unlimited"
			}
			fmt.Printf("%s = %v\n", strings.ReplaceAll(k, "_", " "), v)
		}
	}
}

func teamSetHelp() {
	fmt.Print(`Usage: ptln team set [flags]

Team-wide settings. Owners and admins only.

  --name "<name>"          rename the team
  --review on|off          hold finished work in Review until a human accepts it
  --engine <e>             default engine: claude | codex | gemini | antigravity
                           (or 'none' to leave it to each project)
  --model <m>              default model, e.g. claude-opus-5  ('none' to unset)
  --max-runs <n|none>      how many runs the whole team may have going at once
  --max-runs-machine <n|none>  ... and per machine
  --git-provider <p>       github | gitlab | bitbucket

Read them back with:  ptln team show
`)
}

// normalizeOff maps the words a person types for "unset" onto the empty string the API wants.
func normalizeOff(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none", "off", "unset", "default":
		return ""
	}
	return v
}

func onOff(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off")
}
