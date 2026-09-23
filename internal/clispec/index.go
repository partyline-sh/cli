package clispec

// The two lists `ptln` prints about itself — the grouped COMMANDS block in `ptln help`, and the
// one-line recovery list after an unknown command — are rendered from the registry rather than
// written out by hand.
//
// They were hand-written until an audit found `ptln trigger` in both, a year after the command was
// deleted, and `memory`, `bus` and `feed` in neither, months after they shipped. A list a human
// maintains alongside the thing it describes is a list that is wrong; TestEveryDispatchedCommandHasASpec
// already holds the registry to the dispatcher, so rendering from the registry puts both lists
// inside that guarantee.

import (
	"fmt"
	"io"
	"strings"
)

// Visible is every command a person may type, in registry order.
func Visible() []Spec {
	out := make([]Spec, 0, len(Commands))
	for _, c := range Commands {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	return out
}

// Names is the visible command names, for the line printed after an unknown command.
func Names() []string {
	v := Visible()
	out := make([]string, 0, len(v))
	for _, c := range v {
		out = append(out, c.Name)
	}
	return out
}

// WriteIndex renders the grouped command list: a heading per Group, then `name  summary`.
func WriteIndex(w io.Writer, indent string) {
	group := ""
	for i, c := range Visible() {
		if c.Group != "" && c.Group != group {
			group = c.Group
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s%s\n", indent, group)
		}
		fmt.Fprintf(w, "%s  %-14s %s\n", indent, c.Name, c.Summary)
	}
}

// WrapNames renders Names() as comma-separated lines no wider than width.
func WrapNames(indent string, width int) string {
	var b strings.Builder
	line := indent
	for i, n := range Names() {
		piece := n
		if i < len(Names())-1 {
			piece += ","
		}
		if len(line)+1+len(piece) > width && strings.TrimSpace(line) != "" {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			line = indent
		}
		if strings.TrimSpace(line) != "" {
			line += " "
		}
		line += piece
	}
	b.WriteString(strings.TrimRight(line, " "))
	return b.String()
}
