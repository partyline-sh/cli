package surfacegen

import (
	"bytes"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/surface"
	"partyline.sh/partyline/internal/surfacescan"
)

// The generated reference pages. These are the tier that must never be hand-written, because they
// are pure restatements of what the code already knows — and a hand-written restatement is a
// restatement that will be wrong. Narrative documentation stays hand-written and is governed
// instead by Epic D's staleness stamps.
//
// Every page carries `covers:` front matter naming what it documents, so the D.2 gap check can see
// that these anchors ARE documented and does not report them as holes.

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

// genVocabReference documents every closed vocabulary, including the retry disposition that makes
// the gate codes actionable.
func genVocabReference() []byte {
	var covers []string
	for _, v := range surface.All() {
		covers = append(covers, "vocab:"+v.Name)
	}
	var b bytes.Buffer
	b.WriteString(mdHeader("Vocabularies", "Every closed set of values partyline uses on a boundary.", covers))
	b.WriteString("These are the values the API accepts, the database constrains, and the UI renders. ")
	b.WriteString("Each is declared once in `internal/surface/vocab.go`; the TypeScript unions, the ")
	b.WriteString("Postgres CHECK constraints, and this page are all generated from that declaration.\n")

	for _, v := range surface.All() {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n", v.Name, v.Doc)
		if v.Column != "" {
			fmt.Fprintf(&b, "Stored in `%s`.\n\n", v.Column)
		}
		if v.Classed {
			b.WriteString("| Value | Retry | Meaning |\n|---|---|---|\n")
			for _, t := range v.Terms {
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", t.Key, string(t.Class), oneLine(t.Doc))
			}
			continue
		}
		b.WriteString("| Value | Meaning |\n|---|---|\n")
		for _, t := range v.Terms {
			fmt.Fprintf(&b, "| `%s` | %s |\n", t.Key, oneLine(t.Doc))
		}
	}
	return b.Bytes()
}

// genCLIReference documents every command from the registry. This is what stops `ptln daemon
// --help` and the docs disagreeing, which they did for months.
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

func genAPIReference(s surfacescan.Surface) []byte {
	items := s.OfKind("api")
	var covers []string
	for _, it := range items {
		covers = append(covers, it.Ref)
	}
	var b bytes.Buffer
	b.WriteString(mdHeader("HTTP API", "Every endpoint and the methods it answers.", covers))
	fmt.Fprintf(&b, "%d endpoints, derived from the Next App Router tree — the filesystem IS the route table, ", len(items))
	b.WriteString("so this list cannot drift from what the server serves.\n\n")
	b.WriteString("| Endpoint | Methods |\n|---|---|\n")
	for _, it := range items {
		methods := strings.Join(it.Detail, ", ")
		if methods == "" {
			methods = "_none exported_"
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", it.Name, methods)
	}
	return b.Bytes()
}

func genSchemaReference(s surfacescan.Surface) []byte {
	items := s.OfKind("table")
	var covers []string
	for _, it := range items {
		covers = append(covers, it.Ref)
	}
	var b bytes.Buffer
	b.WriteString(mdHeader("Database schema", "Every table and its columns.", covers))
	fmt.Fprintf(&b, "%d tables, derived by replaying `supabase/migrations/*.sql` in filename order — ", len(items))
	b.WriteString("the same order Postgres applies them.\n\n")
	for _, it := range items {
		fmt.Fprintf(&b, "### %s\n\n", it.Name)
		if it.Source != "" {
			fmt.Fprintf(&b, "Introduced in `%s`.\n\n", it.Source)
		}
		fmt.Fprintf(&b, "`%s`\n\n", strings.Join(it.Detail, "`, `"))
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
