package main

import (
	"os"
	"strings"
	"testing"
)

// The Postgres bootstrap runs its SQL through an UNQUOTED heredoc, because it has to expand
// ${AUTHENTICATOR_PASSWORD}. That means the shell also expands everything else — and a backtick in
// an ordinary SQL COMMENT becomes command substitution.
//
// This shipped. `-- PostgREST logs in as ` + "`authenticator`" + ` and SET ROLEs …` ran
// `authenticator` as a command, and a self-hoster's very first `docker compose up` printed
//
//	00-bootstrap.sh: line 43: authenticator: command not found
//
// against a half-initialised database. One instance had been escaped by hand at some point, which
// fixed that line and left the cause; the next comment someone wrote re-broke it.
//
// So the rule is mechanical and checkable: no backticks between the heredoc markers.
func TestBootstrapHeredocHasNoShellSubstitution(t *testing.T) {
	const path = "deploy/stack/init/00-bootstrap.sh"
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(body), "\n")

	start, end := -1, -1
	for i, l := range lines {
		if start < 0 && strings.Contains(l, "<<-SQL") {
			start = i
			continue
		}
		if start >= 0 && strings.TrimSpace(l) == "SQL" {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("could not find the SQL heredoc in %s — if its shape changed, this guard needs updating rather than deleting", path)
	}

	for i := start + 1; i < end; i++ {
		if strings.Contains(lines[i], "`") {
			t.Errorf("%s:%d has a backtick inside the unquoted SQL heredoc — the shell will run it as a command:\n  %s\nUse 'single quotes' in SQL comments instead.",
				path, i+1, strings.TrimSpace(lines[i]))
		}
	}
}
