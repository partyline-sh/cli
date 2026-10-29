package main

// What each chat member is doing in its own tab, for the room's status lines.
//
// Two sources. The screen says whether the session is working, idle, or stopped on a prompt that
// needs the person ("Do you want to proceed? ❯ 1. Yes"). The session file says what it last did:
// the newest tool call in Claude's transcript or Codex's rollout for the pane's directory.
//
// Claude Code's working line is "✽ Ebbing… (39s · ↓ 2.3k tokens)", with no "esc to interrupt" in
// current versions; codex draws "Working (1m 12s • esc to interrupt)". Both are matched. If a layout
// changes, detection fails toward "idle", never toward a state the session is not in.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
)

type memberActivity struct {
	State   string // "approval", "working" or "idle"
	Verb    string // the engine's own word for what it is doing, from its screen ("Ebbing")
	Elapsed string // the engine's own elapsed time for the current turn ("2m 03s")
	Doing   string // the last tool call, summarised ("running go test ./...")
}

var (
	claudeWorking = regexp.MustCompile(`(?m)^\s*\S\s+([^\s…]+)…\s*\(((?:\d+h\s*)?(?:\d+m\s*)?\d+s)`)
	codexWorking  = regexp.MustCompile(`Working \(((?:\d+h\s*)?(?:\d+m\s*)?\d+s)`)
	// The highlighted option of a permission menu: "❯ 1. Yes", or another option the cursor is on.
	menuFirst  = regexp.MustCompile(`(?m)^\s*[❯›]\s*\d+\.\s+\S`)
	menuOption = regexp.MustCompile(`(?m)^\s*(?:[❯›]\s*)?\d+\.\s+\S`)
	menuWords  = regexp.MustCompile(`(?i)\b(yes|allow|approve|proceed)\b`)
)

// screenActivity classifies the bottom of a pane, given as plain text.
func screenActivity(screen string) memberActivity {
	if menuFirst.MatchString(screen) && len(menuOption.FindAllString(screen, 3)) >= 2 && menuWords.MatchString(screen) {
		return memberActivity{State: "approval"}
	}
	if m := claudeWorking.FindStringSubmatch(screen); m != nil {
		return memberActivity{State: "working", Verb: m[1], Elapsed: m[2]}
	}
	if m := codexWorking.FindStringSubmatch(screen); m != nil {
		return memberActivity{State: "working", Verb: "Working", Elapsed: m[1]}
	}
	if strings.Contains(strings.ToLower(screen), "esc to interrupt") {
		return memberActivity{State: "working"}
	}
	return memberActivity{State: "idle"}
}

// screenBottom is the last n non-empty lines of a screen.
func screenBottom(screen string, n int) string {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append([]string{lines[i]}, kept...)
		}
	}
	return strings.Join(kept, "\n")
}

// probeActivity reads one member's pane and session file.
func probeActivity(p chatPane) memberActivity {
	// The visible screen only, and only its bottom: the working line and permission menus sit just
	// above the input box. Scrollback holds old working lines and quoted text that would match.
	out, err := tmuxCmd("capture-pane", "-p", "-t", p.ID).Output()
	if err != nil {
		return memberActivity{State: "idle"}
	}
	a := screenActivity(screenBottom(string(out), 15))
	switch p.Engine {
	case "claude":
		a.Doing = claudeLastTool(p.Cwd)
	case "codex":
		a.Doing = codexLastTool(p.Cwd)
	}
	return a
}

// claudeLastTool summarises the newest tool call in the newest Claude transcript for dir.
func claudeLastTool(dir string) string {
	if dir == "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	enc := nonAlnum.ReplaceAllString(dir, "-") // Claude names a project folder this way
	f := newestFile(filepath.Join(home, ".claude", "projects", enc, "*.jsonl"))
	if f == "" {
		return ""
	}
	last := ""
	tailLines(f, 128<<10, func(line []byte) {
		var d struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &d) != nil || d.Type != "assistant" {
			return
		}
		for _, c := range d.Message.Content {
			if c.Type == "tool_use" && !chatPlumbing(string(c.Input)) {
				last = clipDoing(summariseTool(c.Name, c.Input))
			}
		}
	})
	return last
}

// chatPlumbing reports whether a tool call is the session talking to the room (ptln chat listen,
// read, send). Showing "Listen for chat messages" as what a session is doing tells you nothing.
func chatPlumbing(input string) bool {
	return strings.Contains(input, "ptln chat ") || strings.Contains(input, "partyline chat ")
}

// clipDoing makes a tool summary one short line.
func clipDoing(s string) string {
	return runewidth.Truncate(oneRow(s), 60, "…")
}

// summariseTool turns a Claude tool call into a few words.
func summariseTool(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return strings.TrimSpace(s) }
	switch name {
	case "Bash":
		if d := str("description"); d != "" {
			return d
		}
		return "running " + str("command")
	case "Read":
		return "reading " + filepath.Base(str("file_path"))
	case "Edit", "MultiEdit":
		return "editing " + filepath.Base(str("file_path"))
	case "Write":
		return "writing " + filepath.Base(str("file_path"))
	case "Grep", "Glob":
		return "searching for " + firstNonEmpty(str("pattern"), str("query"))
	case "WebSearch":
		return "searching the web for " + str("query")
	case "WebFetch":
		return "reading " + str("url")
	case "Agent", "Task":
		return "running a subagent: " + str("description")
	}
	return "using " + name
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

var (
	codexCmd  = regexp.MustCompile(`exec_command\(\{\s*cmd:\s*"((?:[^"\\]|\\.)*)"`)
	codexTool = regexp.MustCompile(`tools\.(\w+)\(`)
)

// codexLastTool summarises the newest tool call in the newest Codex rollout whose session ran in
// dir. Codex records a call as a short script: tools.exec_command({cmd:"..."}) for a command.
func codexLastTool(dir string) string {
	if dir == "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	// Only the last three days of rollouts: they are never pruned, and a member's session is active.
	var files []string
	for d := 0; d < 3; d++ {
		day := time.Now().AddDate(0, 0, -d).Format("2006/01/02")
		more, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", day, "rollout-*.jsonl"))
		files = append(files, more...)
	}
	mtimes := make(map[string]time.Time, len(files))
	for _, f := range files {
		mtimes[f] = modTime(f)
	}
	sort.Slice(files, func(i, j int) bool { return mtimes[files[i]].After(mtimes[files[j]]) })
	for _, f := range files {
		if codexSessionDirCached(f) != dir {
			continue
		}
		last := ""
		tailLines(f, 256<<10, func(line []byte) {
			var d struct {
				Payload struct {
					Type  string `json:"type"`
					Name  string `json:"name"`
					Input string `json:"input"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &d) != nil || !strings.HasSuffix(d.Payload.Type, "_call") {
				return
			}
			if chatPlumbing(d.Payload.Input) {
				return
			}
			if m := codexCmd.FindStringSubmatch(d.Payload.Input); m != nil {
				var cmd string
				if json.Unmarshal([]byte(`"`+m[1]+`"`), &cmd) != nil {
					cmd = m[1]
				}
				last = clipDoing("running " + cmd)
			} else if m := codexTool.FindStringSubmatch(d.Payload.Input); m != nil {
				last = "using " + strings.ReplaceAll(m[1], "__", " ")
			} else if d.Payload.Name != "" {
				last = "using " + d.Payload.Name
			}
		})
		return last
	}
	return ""
}

var (
	codexDirMu    sync.Mutex
	codexDirCache = map[string]string{} // rollout file → the directory its session ran in; never changes
)

func codexSessionDirCached(f string) string {
	codexDirMu.Lock()
	d, ok := codexDirCache[f]
	codexDirMu.Unlock()
	if ok {
		return d
	}
	d = codexSessionDir(f)
	codexDirMu.Lock()
	codexDirCache[f] = d
	codexDirMu.Unlock()
	return d
}

// codexSessionDir reads the directory a Codex session ran in from its first line.
func codexSessionDir(f string) string {
	fh, err := os.Open(f)
	if err != nil {
		return ""
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	if !sc.Scan() {
		return ""
	}
	var d struct {
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(sc.Bytes(), &d)
	return d.Payload.Cwd
}

// tailLines calls fn for each complete line in the last n bytes of f.
func tailLines(f string, n int64, fn func([]byte)) {
	fh, err := os.Open(f)
	if err != nil {
		return
	}
	defer fh.Close()
	if fi, err := fh.Stat(); err == nil && fi.Size() > n {
		_, _ = fh.Seek(-n, 2)
	}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	first := true
	for sc.Scan() {
		if first { // the first line of a tail is usually cut short
			first = false
			continue
		}
		fn(sc.Bytes())
	}
}

func newestFile(glob string) string {
	files, _ := filepath.Glob(glob)
	best, bestT := "", time.Time{}
	for _, f := range files {
		if t := modTime(f); t.After(bestT) {
			best, bestT = f, t
		}
	}
	return best
}

func modTime(f string) time.Time {
	if fi, err := os.Stat(f); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// chatFreeNote is the line printed when a member that was working becomes idle.
func chatFreeNote(name string, worked time.Duration) string {
	return fmt.Sprintf("\x1b[2m%s is free (worked %s)\x1b[0m", name, chatElapsed(worked.Round(time.Second)))
}
