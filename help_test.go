package main

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/clispec"
)

func helpText(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	helpMain()
	os.Stdout = old
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// `ptln help` is the first thing a person runs. After the server was removed it kept advertising
// `ptln start`, `ptln server install` and `ptln login` for a week, because the man page is tested
// for phantoms and the help prose was not. Same check, same regex, pointed at the help.
func TestHelpNamesNoPhantomCommand(t *testing.T) {
	text := helpText(t)
	real := map[string]bool{}
	for _, c := range clispec.Commands {
		real[c.Name] = true
		for _, a := range c.Aliases {
			real[a] = true
		}
	}
	var phantom []string
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`ptln ([a-z][a-z-]{1,20})`).FindAllStringSubmatch(text, -1) {
		v := m[1]
		if real[v] || seen[v] {
			continue
		}
		seen[v] = true
		phantom = append(phantom, v)
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Errorf("ptln help names commands that do not exist: %v", phantom)
	}
	// Concepts that left with the server. Naming one here sends a person looking for a feature
	// that is not there.
	for _, gone := range []string{"server install", "login <url>", "context-threads", "MCP TOOLS", "join link", "daemon", "crank"} {
		if strings.Contains(text, gone) {
			t.Errorf("ptln help still mentions %q", gone)
		}
	}
}
