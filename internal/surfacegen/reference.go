package surfacegen

// The hand-readable references under docs/reference/. These used to share a file with the vocab,
// API and schema references; those described the server and went with it.

import (
	"bytes"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/surfacescan"
)

func mdHeader(title, desc string, covers []string) string {
	var b bytes.Buffer
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "description: %s\n", desc)
	fmt.Fprintf(&b, "generated: true\n")
	if len(covers) > 0 {
		fmt.Fprintf(&b, "covers: [%s]\n", strings.Join(covers, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(header("<!--") + "-->\n\n")
	return b.String()
}

func genCLIReference() []byte {
	var covers []string
	for _, c := range clispec.Commands {
		if !c.Hidden {
			covers = append(covers, "cli:"+c.Name)
		}
	}
	var b bytes.Buffer
	b.WriteString(mdHeader("CLI reference", "Every ptln command, its flags, and its subcommands.", covers))
	b.WriteString("Generated from `internal/clispec/registry.go`, which is the same declaration ")
	b.WriteString("`ptln <command> --help` renders — so this page and the CLI cannot disagree.\n\n")

	b.WriteString("| Command | What it does |\n|---|---|\n")
	for _, c := range clispec.Commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "| [`%s`](#%s) | %s |\n", c.Name, c.Name, c.Summary)
	}

	for _, c := range clispec.Commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n", c.Name, c.Summary)
		if len(c.Aliases) > 0 {
			fmt.Fprintf(&b, "Also: %s\n\n", "`"+strings.Join(c.Aliases, "`, `")+"`")
		}
		b.WriteString("```\n")
		if len(c.Usage) == 0 {
			fmt.Fprintf(&b, "ptln %s\n", c.Name)
		}
		for _, u := range c.Usage {
			b.WriteString(u + "\n")
		}
		b.WriteString("```\n")
		if len(c.Subs) > 0 {
			b.WriteString("\n| Subcommand | |\n|---|---|\n")
			for _, s := range c.Subs {
				name, doc, _ := strings.Cut(s, ":")
				fmt.Fprintf(&b, "| `%s` | %s |\n", strings.TrimSpace(name), strings.TrimSpace(doc))
			}
		}
		if len(c.Flags) > 0 {
			b.WriteString("\n| Flag | |\n|---|---|\n")
			for _, f := range c.Flags {
				left := "--" + f.Name
				if f.Arg != "" {
					left += " " + f.Arg
				}
				fmt.Fprintf(&b, "| `%s` | %s |\n", left, f.Doc)
			}
		}
	}
	return b.Bytes()
}

func genEnvReference(s surfacescan.Surface) []byte {
	items := s.OfKind("env")
	var covers []string
	for _, it := range items {
		covers = append(covers, it.Ref)
	}
	var b bytes.Buffer
	b.WriteString(mdHeader("Environment variables", "Every variable the code reads, and where.", covers))
	fmt.Fprintf(&b, "%d variables, derived from their call sites. This is the self-host configuration ", len(items))
	b.WriteString("surface: there is no single declaration of it in the code, which is why the deploy ")
	b.WriteString("`env.example` originally had to be reconstructed by hand.\n\n")
	b.WriteString("Presence here does not mean a variable is REQUIRED — many are optional and their ")
	b.WriteString("feature degrades cleanly when unset. See the self-host guide for which is which.\n\n")
	b.WriteString("| Variable | Read by |\n|---|---|\n")
	for _, it := range items {
		fmt.Fprintf(&b, "| `%s` | `%s` |\n", it.Name, strings.Join(it.Detail, "`, `"))
	}
	return b.Bytes()
}
