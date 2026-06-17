package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/ptymux"
)

// muxMain — `ptln mux [<id|prefix>...]` is the same persistent launcher as `ptln llms`,
// but pre-opens the given sessions straight into live windows. Bare `ptln mux` opens the
// launcher (home). Inside, ctrl-o drops back to the launcher, ctrl-\ n/p cycles.
func muxMain(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		muxUsage()
		return
	}
	sessions := collectSessions()
	var specs []ptymux.Spec
	for _, q := range args {
		s, ok := matchSession(sessions, q)
		if !ok {
			fatal(fmt.Errorf("ptln mux: no session matches %q (run `ptln llms` for ids)", q))
		}
		if s.resumeArgv == nil {
			fatal(fmt.Errorf("ptln mux: %s sessions aren't resumable (no recorded path)", s.Tool))
		}
		specs = append(specs, ptymux.Spec{Label: muxLabel(s), Key: s.ID, Argv: s.resumeArgv, Dir: s.resumeDir})
	}
	if err := runLLMSApp(specs); err != nil {
		fatal(fmt.Errorf("ptln mux: %w", err))
	}
}

// matchSession resolves an id or unique prefix against the session inventory (mirrors
// aiResume's matching).
func matchSession(sessions []aiSession, q string) (aiSession, bool) {
	for _, s := range sessions {
		if s.ID == q {
			return s, true
		}
	}
	var hit aiSession
	n := 0
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, q) {
			hit, n = s, n+1
		}
	}
	return hit, n == 1
}

func muxLabel(s aiSession) string {
	if s.Cwd != "" {
		return s.Tool + " · " + filepath.Base(s.Cwd)
	}
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return s.Tool + " · " + id
}

func muxUsage() {
	fmt.Println(`Usage: ptln mux [<id>...]

The persistent session launcher (same as ` + "`ptln llms`" + `), pre-opening the given
sessions into live windows. Bare ` + "`ptln mux`" + ` (or ` + "`ptln llms`" + `) opens the launcher.
Ids come from the launcher list (id or unique prefix). Inside a live session:
  ctrl-o         drop back to the launcher (session keeps running)
  ctrl-\ n / p   next / previous session
  ctrl-\ 1-9     jump to session N
  ctrl-\ x       close the focused session
  ctrl-\ q       quit`)
}
