// Deleting a session from the launcher.
//
// The launcher indexes each engine's OWN session store read-only, and that is why partyline would
// not touch disk state it does not own: `x` used to set an `archived` flag in our sidecar, hiding
// the row and leaving the engine's file where it was. That failed in practice. Hiding a session and
// deleting one looked identical at the moment you pressed the key — the row vanished either way —
// and because nothing ever left, the list only grew.
//
// So `x` deletes, and the safety rule moved from the outcome to the mechanism: it MOVES a session's
// files into a partyline-owned trash directory instead of unlinking them. The row leaves the list
// for good, because the collectors glob the stores and a file that is no longer there cannot come
// back. The conversation is still on disk under ~/.partyline/trash/ with a manifest recording where
// each file came from, so pressing the key on the wrong row of a long list costs a `mv` rather than
// a conversation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"partyline.sh/partyline/internal/brand"
)

func sessionTrashDir() string { return filepath.Join(stateDir(), "trash") }

// deleteFacts is what the confirmation shows. It is deliberately the reasons NOT to go ahead:
// a title and an id tell you nothing about whether this is the row you meant, whereas "3
// uncommitted in a worktree you used 10 minutes ago" does. Gathered before anything moves.
type deleteFacts struct {
	Recent     bool   // used within the last day
	Age        string // "2h" — how long since it was last active
	Worktree   string // git worktree basename when the session sits in a live one
	Branch     string // branch at its cwd
	Dirty      int    // uncommitted files there right now (-1 = unknown)
	Messages   int    // turns in the transcript
	LiveSibs   int    // OTHER sessions running in the same project
	Gone       bool   // its directory no longer exists
	Paths      []string
	TrashUnder string
}

// gatherDeleteFacts collects the context for the confirmation. peers is the rest of the list, used
// to answer "is anything else in this project still working?" — deleting one session of an active
// tree is a different decision from deleting a stray one.
func gatherDeleteFacts(s aiSession, peers []aiSession) deleteFacts {
	f := deleteFacts{Dirty: -1, TrashUnder: sessionTrashDir()}
	f.Paths, _ = sessionFiles(s)
	if !s.LastActive.IsZero() {
		// humanAge is what the detail pane beside this modal uses. shortDuration caps at hours and
		// rendered a month-old session as "644h54m ago" next to a panel reading "26d ago".
		f.Age, f.Recent = humanAge(s.LastActive), time.Since(s.LastActive) < 24*time.Hour
	}
	// A worktree that still exists is an "active tree": the branch is checked out somewhere and
	// may hold work. A dead one is the case `ptln llms prune` exists for.
	if s.WtName != "" && !s.DeadWt {
		f.Worktree = s.WtName
	}
	if d := detailFor(s); d != nil {
		f.Branch, f.Dirty, f.Messages, f.Gone = d.Branch, d.Dirty, d.Messages, d.Gone
	}
	for _, p := range peers {
		if p.ID != s.ID && p.Live && p.ProjDir == s.ProjDir && s.ProjDir != "" {
			f.LiveSibs++
		}
	}
	return f
}

// sessionFiles returns every path that belongs to a session.
//
// ok=false means this tool keeps its sessions somewhere a single one cannot be carved out of, so
// deleting it would mean writing into another tool's database rather than moving a file.
func sessionFiles(s aiSession) ([]string, bool) {
	switch s.Tool {
	case "claude", "codex", "gemini":
		// One file per session: ~/.claude/projects/<cwd>/<id>.jsonl, codex's dated rollout-*.jsonl,
		// gemini's chats/session-*.json. storePath is that file.
		if s.storePath == "" {
			return nil, false
		}
		return []string{s.storePath}, true
	case "antigravity":
		// The conversation is conversations/<id>.db; brain/<id>/ holds its working state and
		// storePath points at a transcript INSIDE that tree. Moving only the transcript would
		// leave a resumable session with no title, which reads as corruption rather than deletion.
		base := filepath.Join(sessionRootHome(s), ".gemini", "antigravity-cli")
		return []string{
			filepath.Join(base, "conversations", s.ID+".db"),
			filepath.Join(base, "brain", s.ID),
		}, true
	}
	// `llm` keeps every conversation in one shared SQLite log and is indexed through `llm logs
	// list`, not a file glob. There is no per-session file to move.
	return nil, false
}

// sessionRootHome is the HOME whose store a session was found under. An adopted root is a whole
// other home directory (see llms_roots.go); its sessions must be addressed there, not here.
func sessionRootHome(s aiSession) string {
	if s.root != "" {
		return s.root
	}
	home, _ := os.UserHomeDir()
	return home
}

// deleteModalLines renders the confirmation body. Each risk gets its own line and its own colour,
// because a wall of grey text is read as "press y" — the point of the modal is that the one fact
// which should stop you is the one that catches the eye.
func deleteModalLines(s aiSession, f deleteFacts) []string {
	grey := func(t string) string { return "\x1b[38;5;245m" + t + "\x1b[0m" }
	warn := func(t string) string { return "\x1b[1;38;5;215m" + t + "\x1b[0m" }

	lines := []string{
		"\x1b[1;38;5;231m" + brand.Clip(s.Title, 46) + "\x1b[0m",
	}
	id := toolLabel(s.Tool) + " · " + short(s.ID, 8)
	if f.Messages > 0 {
		id += fmt.Sprintf(" · %d messages", f.Messages)
	}
	lines = append(lines, grey(id), "")

	if f.Gone {
		lines = append(lines, grey("✗ its directory no longer exists"))
	} else if f.Branch != "" {
		b := "\x1b[38;5;114m⎇ " + brand.Clip(f.Branch, 28) + "\x1b[0m"
		if f.Dirty > 0 {
			b += "  " + warn(fmt.Sprintf("⚠ %d uncommitted", f.Dirty))
		} else if f.Dirty == 0 {
			b += "  \x1b[38;5;108m✓ clean\x1b[0m"
		}
		lines = append(lines, b)
	}
	if f.Worktree != "" {
		lines = append(lines, warn("⚠ in an active worktree: "+brand.Clip(f.Worktree, 28)))
	}
	if f.Age != "" {
		used := "last used " + f.Age + " ago"
		if f.Recent {
			lines = append(lines, warn("⚠ "+used))
		} else {
			lines = append(lines, grey(used))
		}
	}
	if f.LiveSibs == 1 {
		lines = append(lines, warn("⚠ another session in this project is running"))
	} else if f.LiveSibs > 1 {
		lines = append(lines, warn(fmt.Sprintf("⚠ %d other sessions here are running", f.LiveSibs)))
	}
	return append(lines, "",
		grey("recoverable from "+brand.Clip(f.TrashUnder, 46)),
		"",
		"\x1b[1;38;5;203m[y] delete\x1b[0m   \x1b[38;5;240m[n] cancel\x1b[0m")
}

// trashedFile records one moved path so a deletion can be undone by hand.
type trashedFile struct {
	From string `json:"from"` // where it was
	Name string `json:"name"` // its basename inside the trash entry
}

// trashManifest is written beside the moved files. It exists so that "restore this" is a question
// the directory can answer on its own, months later, without the launcher.
type trashManifest struct {
	Tool      string        `json:"tool"`
	ID        string        `json:"id"`
	Title     string        `json:"title,omitempty"`
	Cwd       string        `json:"cwd,omitempty"`
	DeletedAt time.Time     `json:"deleted_at"`
	Files     []trashedFile `json:"files"`
}

// deleteSession moves a session's files to the trash and forgets its sidecar entry. It returns the
// trash directory so the caller can tell the user where the conversation went.
//
// A LIVE session is refused. The engine holds its store open and rewrites it on every turn, so
// moving the file mid-turn either gets it recreated underneath us — leaving the row in the list and
// the trash entry a truncated copy — or loses the turn in flight. Close it first.
func deleteSession(s aiSession, meta map[string]sessMeta) (string, error) {
	if s.Live {
		return "", fmt.Errorf("%s is running — close it first, then delete", short(s.ID, 8))
	}
	paths, ok := sessionFiles(s)
	if !ok {
		return "", fmt.Errorf("%s sessions live in one shared store, not a file each — nothing to delete individually", toolLabel(s.Tool))
	}
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(sessionTrashDir(), stamp+"-"+s.Tool+"-"+short(s.ID, 8))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	man := trashManifest{Tool: s.Tool, ID: s.ID, Title: s.Title, Cwd: s.Cwd, DeletedAt: time.Now()}
	moved := 0
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			continue // already gone — deleting it is not a failure
		}
		name := filepath.Base(p)
		if err := moveInto(p, filepath.Join(dir, name)); err != nil {
			return "", fmt.Errorf("move %s: %w", name, err)
		}
		man.Files = append(man.Files, trashedFile{From: p, Name: name})
		moved++
	}
	if moved == 0 {
		// Nothing was on disk. Clean up the empty entry rather than leaving a directory that
		// claims a deletion happened.
		_ = os.Remove(dir)
		return "", fmt.Errorf("no session files found on disk for %s", short(s.ID, 8))
	}
	if b, err := json.MarshalIndent(man, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600)
	}
	// Drop our own curation state too, or a pin or rename outlives the session it
	// described and the sidecar grows a row that nothing will ever match again.
	if meta != nil {
		delete(meta, s.ID)
		saveLLMMeta(meta)
	}
	return dir, nil
}

// moveInto renames src to dst, falling back to copy-then-remove when they are on different
// filesystems. An adopted root can be any directory, including a mount that os.Rename refuses to
// cross, and that must not be the thing that makes delete fail.
func moveInto(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// copyTree copies a directory recursively, preserving the executable bit.
//
// Used by the delete path's trash move when os.Rename cannot be used because source and trash are
// on different filesystems. It lived in the skill library that is now deleted.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if info, e := d.Info(); e == nil && info.Mode()&0o111 != 0 {
			perm = 0o755
		}
		return os.WriteFile(target, b, perm)
	})
}
