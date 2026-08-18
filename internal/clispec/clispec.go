package clispec

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// cmdspec.go — one declaration of what every CLI command is and what flags it takes.
//
// WHY THIS EXISTS. Flag parsing is ad-hoc per command: a `switch` over `case "--branch":` inside
// each Main function, with the flag names, their arguments, and their documentation written down
// separately (or not at all). Nothing in the program knows what flags a command accepts, and the
// consequences are exactly what you would predict:
//
//   - `ptln work --help` did not print help. It STARTED A WORKER, because --help was never a case
//     in work's parser and fell through to "treat the rest as the task".
//   - `ptln daemon --help` advertised "Epic R remote-launch — MVP, in progress" long after the
//     daemon shipped, because that string lives in a hand-written help blob nothing checks.
//
// The registry below is the single place a command's shape is declared. Right now it OWNS help and
// nothing else — each command keeps its existing parser (a rewrite of 44 parsers in one change
// would be reckless), so this can land without behavioural risk. Parsing migrates command by
// command afterwards, and the spec is already the thing that documents the result.
//
// It is also the CLI facet of the surface extraction (Epic S): docs coverage and the generated man
// page read this, so a new command cannot be undocumented by accident.

// Flag is one flag. Arg is the placeholder shown in help ("<id>", "N"); empty means boolean.
type Flag struct {
	Name string
	Arg  string
	Doc  string
}

// Spec is one top-level command.
type Spec struct {
	Name    string
	Aliases []string
	// Summary is one line, shown in the command list.
	Summary string
	// Usage lines are shown verbatim under USAGE. Omit to synthesise a single default line.
	Usage []string
	Flags []Flag
	// Subs are subcommand names with a one-line description each, in "name: description" form.
	Subs []string
	// Hidden keeps a command out of the user-facing list. Used for the stdio protocol
	// subcommands (MCP servers, hooks) that exist for other programs to spawn, not for people.
	Hidden bool
	// PassThrough marks a command that forwards its remaining arguments to a child process.
	// `ptln new claude --help` must reach claude, so we must NOT intercept it.
	PassThrough bool
}

// AllFlagNames returns the declared flags with their leading dashes.
func (c Spec) AllFlagNames() []string {
	out := make([]string, 0, len(c.Flags))
	for _, f := range c.Flags {
		out = append(out, "--"+f.Name)
	}
	return out
}

// Lookup resolves a name or alias to its spec.
func Lookup(name string) (Spec, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
		for _, a := range c.Aliases {
			if a == name {
				return c, true
			}
		}
	}
	return Spec{}, false
}

// WantsHelp reports whether argv is a request for a command's help — `ptln <cmd> --help`, `-h`, or
// `ptln <cmd> help`.
//
// Deliberately narrow: only the argument IMMEDIATELY after the command counts. `ptln crank --file
// x --help` is not intercepted, because at that point the user is mid-invocation and the command's
// own parser should have its say. And a PassThrough command is never intercepted at all — `ptln new
// claude --help` is asking claude for help, not us.
func WantsHelp(spec Spec, argv []string) bool {
	if spec.PassThrough || len(argv) < 2 {
		return false
	}
	switch argv[1] {
	case "--help", "-h", "help":
		return true
	}
	return false
}

// MaybeHelp prints a command's help and reports whether it did. Called from the dispatcher before
// the command runs, which is what makes --help uniform across all 44 commands without touching 44
// parsers.
func MaybeHelp(name string, argv []string) bool {
	spec, ok := Lookup(name)
	if !ok || !WantsHelp(spec, argv) {
		return false
	}
	PrintHelp(os.Stdout, spec)
	return true
}

// PrintHelp renders one command's help from its spec.
func PrintHelp(w io.Writer, c Spec) {
	fmt.Fprintf(w, "ptln %s — %s\n", c.Name, c.Summary)
	fmt.Fprintln(w, "\nUSAGE")
	if len(c.Usage) == 0 {
		fmt.Fprintf(w, "  ptln %s\n", c.Name)
	}
	for _, u := range c.Usage {
		fmt.Fprintf(w, "  %s\n", u)
	}
	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "\nALIASES\n  %s\n", strings.Join(c.Aliases, ", "))
	}
	if len(c.Subs) > 0 {
		fmt.Fprintln(w, "\nSUBCOMMANDS")
		for _, s := range c.Subs {
			name, doc, _ := strings.Cut(s, ":")
			fmt.Fprintf(w, "  %-14s %s\n", strings.TrimSpace(name), strings.TrimSpace(doc))
		}
	}
	if len(c.Flags) > 0 {
		fmt.Fprintln(w, "\nFLAGS")
		for _, f := range c.Flags {
			left := "--" + f.Name
			if f.Arg != "" {
				left += " " + f.Arg
			}
			fmt.Fprintf(w, "  %-24s %s\n", left, f.Doc)
		}
	}
	fmt.Fprintln(w, "\nSee `ptln help` for every command.")
}
