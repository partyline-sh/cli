package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"partyline.sh/partyline/internal/ptymux"
)

// Engine-spawned forks: the sessions partyline never hears about.
//
// A tab stores the engine's session id and resumes exactly that. That pin is correct and must
// stay — a named tab has to mean ONE conversation, forever, or a shared session and an
// `ask_session` by name both stop meaning anything.
//
// But the engine can mint a NEW session at runtime and move the conversation into it. A slash
// command inside a session runs `--fork-session`, which forks the transcript, hands the child to
// the engine's own daemon, and tells partyline nothing. The tab keeps pointing at the parent —
// faithfully, by design — while the human keeps typing into the child.
//
// Observed: a fork ran for over a day, the human talked to it the whole time, and `ptln --resume`
// restored the PARENT. Same label, same directory, all the history up to the fork point — and
// none of the last day's work. It reads as "the AI forgot everything", which is the single worst
// thing this product can do, and the transcript was fine the entire time.
//
// So: pin the parent, and ADOPT the fork as its own tab. Both halves are needed. Pinning alone is
// what stranded the fork; following alone would make a tab's name cover a lineage rather than a
// conversation.

// forkOf is one engine-spawned session and the tab it came from.
type forkOf struct {
	SessionID string // the fork's OWN id — what a resume would target
	ParentKey string // the tab it was forked from
	Cwd       string
	Held      bool // still owned by the engine's daemon; a plain --resume would be refused
}

// engineRosterPath is the ENGINE's own daemon roster — distinct from partyline's session roster
// in ask_session_store.go, which is our own publication for list_sessions. Named apart because
// confusing the two would be easy and the failure would be silent.
func engineRosterPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "daemon", "roster.json")
}

// discoverForks reads the engine's own roster and reports the forks it is running.
//
// Deliberately reads the roster rather than guessing from the session store. The roster records
// `fork: true` and the PARENT'S TRANSCRIPT PATH, which is an exact parent→child link. Inferring
// one instead — "a newer session in the same directory" — would adopt every unrelated session
// that happens to share a repo, and adopting the wrong conversation under a trusted tab name is
// worse than adopting none.
//
// Claude-specific today, because it is the only engine that both forks and publishes a roster.
// Every failure returns nil: an unreadable roster must degrade to exactly today's behaviour, never
// break the resume path that this exists to protect.
func discoverForks() []forkOf {
	p := engineRosterPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var doc struct {
		Workers map[string]struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			PID       int    `json:"pid"`
			Dispatch  struct {
				Launch struct {
					Fork      bool   `json:"fork"`
					SessionID string `json:"sessionId"` // PATH to the parent's transcript
				} `json:"launch"`
			} `json:"dispatch"`
		} `json:"workers"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}

	var out []forkOf
	for _, w := range doc.Workers {
		if !w.Dispatch.Launch.Fork || w.SessionID == "" {
			continue
		}
		parent := parentKeyFromPath(w.Dispatch.Launch.SessionID)
		if parent == "" || parent == w.SessionID {
			continue
		}
		out = append(out, forkOf{
			SessionID: w.SessionID,
			ParentKey: parent,
			Cwd:       w.Cwd,
			Held:      pidAlive(w.PID),
		})
	}
	return out
}

// parentKeyFromPath turns a transcript path into the session id it holds.
// "…/projects/-Users-x/3ab9111a-….jsonl" → "3ab9111a-…".
func parentKeyFromPath(p string) string {
	if p == "" {
		return ""
	}
	base := filepath.Base(p)
	if !strings.HasSuffix(base, ".jsonl") {
		return ""
	}
	return strings.TrimSuffix(base, ".jsonl")
}

// pidAlive reports whether a process still exists. Signal 0 checks for existence without
// delivering anything.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// short8 is the id prefix the engine's own tooling shows, so what partyline prints can be matched
// against `claude agents` without the human translating between two forms of the same id.
func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// forkLabel names an adopted tab after its parent, so the relationship is visible at a glance
// instead of appearing as an unexplained session id.
func forkLabel(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "fork"
	}
	return parent + " ⑂"
}

// adoptForks returns extra Specs for forks of the given live tabs.
//
// The parent's Spec is NEVER touched. Adoption only ever ADDS a tab, so the guarantee that a named
// tab reloads the conversation it was created for survives untouched — that is the whole point of
// doing it this way rather than repointing the parent at its fork.
func adoptForks(live []ptymux.Spec) []ptymux.Spec {
	if len(live) == 0 {
		return nil
	}
	forks := discoverForks()
	if len(forks) == 0 {
		return nil
	}
	byKey := make(map[string]ptymux.Spec, len(live))
	for _, s := range live {
		byKey[s.Key] = s
	}

	var out []ptymux.Spec
	seen := make(map[string]bool)
	for _, f := range forks {
		parent, ok := byKey[f.ParentKey]
		if !ok {
			continue // not a fork of anything we have open
		}
		// Already open as its own tab, or already adopted this pass. Adopting twice would put the
		// same conversation on the switchboard under two names.
		if _, live := byKey[f.SessionID]; live || seen[f.SessionID] {
			continue
		}
		seen[f.SessionID] = true

		sp := parent // inherit dir, engine, model, thread — a fork is the same work
		sp.Key = f.SessionID
		sp.Label = forkLabel(parent.Label)
		sp.Argv = forkArgv(parent.Argv, f.SessionID)
		if f.Cwd != "" {
			sp.Dir = f.Cwd
		}
		out = append(out, sp)
	}
	return out
}

// forkArgv rewrites the parent's resume command to target the fork, keeping every other flag —
// permission mode, MCP config, model. A fork inherits the posture it was forked under; rebuilding
// the argv from defaults would silently drop the flags the human chose.
func forkArgv(parent []string, id string) []string {
	out := append([]string(nil), parent...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--resume" {
			out[i+1] = id
			return out
		}
	}
	// No --resume to rewrite (a fresh session's argv). Target the fork explicitly rather than
	// returning something that would reopen the PARENT under the fork's name.
	return append(out, "--resume", id)
}

// heldForks lists adopted sessions the engine's daemon still owns.
//
// These cannot be restored by `--resume`: the engine refuses to resume a session that is already
// live, which is exactly how the original incident stayed invisible. Restoring the set silently
// minus these would recreate the bug with better manners, so the resume path names them instead.
func heldForks(specs []ptymux.Spec) []forkOf {
	if len(specs) == 0 {
		return nil
	}
	want := make(map[string]bool, len(specs))
	for _, s := range specs {
		want[s.Key] = true
	}
	var out []forkOf
	for _, f := range discoverForks() {
		if f.Held && want[f.SessionID] {
			out = append(out, f)
		}
	}
	return out
}
