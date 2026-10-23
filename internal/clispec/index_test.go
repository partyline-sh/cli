package clispec

import (
	"bytes"
	"strings"
	"testing"
)

// Every command a person can type must appear in the list `ptln help` prints. This is the guard
// that `ptln trigger` (deleted) and `memory`, `bus`, `feed` (shipped) both got past when the list
// was hand-written.
func TestIndexCoversEveryVisibleCommand(t *testing.T) {
	var b bytes.Buffer
	WriteIndex(&b, "  ")
	for _, c := range Visible() {
		if !strings.Contains(b.String(), "  "+c.Name+" ") {
			t.Errorf("`ptln help` index omits %q", c.Name)
		}
	}
}

func TestIndexNamesNothingHidden(t *testing.T) {
	var b bytes.Buffer
	WriteIndex(&b, "  ")
	for _, c := range Commands {
		if !c.Hidden {
			continue
		}
		if strings.Contains(b.String(), "  "+c.Name+" ") {
			t.Errorf("`ptln help` index names the hidden command %q", c.Name)
		}
	}
}

// Every visible command opens under a heading, so no command can land in an unlabelled void when
// one is inserted above the first Group in the registry.
func TestEveryVisibleCommandHasAGroup(t *testing.T) {
	group := ""
	for _, c := range Visible() {
		if c.Group != "" {
			group = c.Group
		}
		if group == "" {
			t.Fatalf("%q appears before any Group heading", c.Name)
		}
	}
}

func TestWrapNamesListsEveryCommand(t *testing.T) {
	out := WrapNames("  ", 92)
	for _, n := range Names() {
		if !strings.Contains(out, n) {
			t.Errorf("unknown-command list omits %q", n)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 92 {
			t.Errorf("line over 92 columns: %q", line)
		}
	}
}
