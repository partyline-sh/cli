// `ptln llms` — a local, cross-tool index of your AI CLI sessions.
//
// The problem this solves: you accumulate a graveyard of half-finished sessions
// across Claude Code, Codex, Gemini, (and groq-via-`llm`), spread over projects.
// Each tool persists + resumes locally, but there's no single place to *see* them
// or jump back in. This is that place — `ptln llms ls` lists every session
// across tools, newest first; `ptln llms resume <id>` hands you back into one.
//
// Tier 1 is deliberately LOCAL only: no daemon, no backend, no network. It reads
// each tool's own on-disk session store (verified formats below) and, on resume,
// chdir's to the recorded cwd and exec's the tool's native resume command.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-runewidth"
)

type aiSession struct {
	Tool       string    // claude | codex | gemini | antigravity | llm
	ID         string    // the tool's native session id
	Cwd        string    // recorded working dir ("" if the tool doesn't store one)
	Title      string    // first user message, trimmed
	LastActive time.Time // file mtime / recorded last-update
	Live       bool      // active right now (store written within the last ~45s)
	storePath  string    // path to the on-disk session file (for lazy detail load)
	resumeDir  string    // chdir here before resuming ("" => current dir)
	resumeArgv []string  // exec this to resume; nil => not resumable (list-only)
	Status     string    // live sessions only: "waiting" (your move) | "active" (working)
}

func llmsMain(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		aiBrowse() // interactive menu on a TTY, flat list otherwise
	case "ls", "list":
		aiList(args[1:])
	case "--resume", "--restore":
		// Reopen the whole set of sessions that were live when you last quit (same
		// permission levels). Distinct from `resume <id>`, which resumes ONE session.
		resumeWorkspace()
	case "resume", "open":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ptln llms resume <id>   (run `ptln llms` to see ids)"))
		}
		aiResume(args[1])
	case "new":
		// Start a FRESH session of an engine (claude|codex|gemini|antigravity) in the mux —
		// the new-session counterpart to resume.
		llmsNew(args[1:])
	case "-h", "--help", "help":
		fmt.Println("ptln llms — browse, run, and switch between your AI CLI sessions (local)")
		fmt.Println("  ptln llms                interactive launcher (arrows; space select, ⏎ open, o new tab)")
		fmt.Println("  ptln llms <id> [<id>…]   open these sessions into one multiplexed terminal")
		fmt.Println("  ptln llms --resume       reopen all sessions from last time (same models + permissions)")
		fmt.Println("  ptln llms ls [--all]     flat list, newest first (--all shows agent/automated)")
		fmt.Println("  ptln llms resume <id>    resume ONE session in this terminal (no multiplexer)")
		fmt.Println("  ptln llms new <tool>     start a FRESH session — claude | codex | gemini | antigravity")
		fmt.Println("  ptln llms new <tool> --thread <id>   attach it to a context thread (shared context)")
		fmt.Println("  Inside: ctrl-\\ ←/→ or 1-9 switch · ctrl-\\ [ scrollback · ctrl-\\ o launcher · ctrl-\\ q quit")
	default:
		// `ptln llms <id> [<id>…]` opens the given sessions into the multiplexer.
		aiOpenInMux(args)
	}
}

// collectSessions gathers from every adapter. A broken/absent store for one tool
// must never sink the whole list, so each adapter swallows its own errors and
// returns what it could read.
func collectSessions() []aiSession {
	home, _ := os.UserHomeDir()
	beginScanPass()     // load the cwd/title scan cache (keyed by file size)
	defer endScanPass() // prune deleted sessions + persist any new scans
	var all []aiSession
	all = append(all, claudeSessions(home)...)
	all = append(all, codexSessions(home)...)
	all = append(all, geminiSessions(home, hashToPath(home, all))...)
	all = append(all, antigravitySessions(home)...)
	all = append(all, llmSessions()...)
	// "Live" = the store was written in the last ~45s, i.e. the session is
	// actively running right now (cheap, mtime-based — no process inspection).
	now := time.Now()
	for i := range all {
		all[i].Live = !all[i].LastActive.IsZero() && now.Sub(all[i].LastActive) < 45*time.Second
	}
	sort.Slice(all, func(i, j int) bool { return all[i].LastActive.After(all[j].LastActive) })
	// Dedupe by tool+id keeping the newest — gemini checkpoints one session into
	// multiple chat files, which would otherwise show as duplicate rows.
	seen := map[string]bool{}
	dedup := all[:0]
	for _, s := range all {
		k := s.Tool + ":" + s.ID
		if seen[k] {
			continue
		}
		seen[k] = true
		dedup = append(dedup, s)
	}
	// Cheap "is it waiting on you?" guess — only for live sessions (a small set, so
	// the extra tail read is bounded). If the last persisted message is the
	// assistant's, the turn ended and it's your move; otherwise it's still working.
	// No process inspection, no daemon.
	for i := range dedup {
		if dedup[i].Live {
			dedup[i].Status = liveStatus(dedup[i])
		}
	}
	applyCwdOverrides(dedup, loadLLMMeta())
	return dedup
}

// applyCwdOverrides rewrites each session's Cwd to its recorded CwdOverride (recover modal → "point
// to new location") when that path still exists. A session's cwd comes from the tool's own store,
// which can't be updated when a dir MOVES — applying the override at load means every consumer
// (gone-detection, resume, capture) transparently uses the new path. A stale override (the new path
// itself vanished) is IGNORED so the real (gone) cwd falls through and recovery is offered again,
// rather than silently pointing at nothing.
func applyCwdOverrides(sessions []aiSession, meta map[string]sessMeta) {
	for i := range sessions {
		ov := meta[sessions[i].ID].CwdOverride
		if ov == "" {
			continue
		}
		if fi, err := os.Stat(ov); err == nil && fi.IsDir() {
			sessions[i].Cwd = ov
		}
	}
}

// liveStatus guesses whether a live session is waiting on you ("waiting") or still
// working ("active"), from the role of its last persisted message. claude/codex
// only; others fall back to "active".
func liveStatus(s aiSession) string {
	if lastMsgRole(s) == "assistant" {
		return "waiting"
	}
	return "active"
}

// lastMsgRole tail-reads a session store and returns the role of its last message
// (cheap: only the file's tail). "" if unknown.
func lastMsgRole(s aiSession) string {
	if s.storePath == "" {
		return ""
	}
	var lm lineMsg
	switch s.Tool {
	case "claude":
		lm = claudeLineMsg
	case "codex":
		lm = codexLineMsg
	default:
		return ""
	}
	b := tailBytes(s.storePath, 64<<10)
	ls := strings.Split(string(b), "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if ls[i] == "" {
			continue
		}
		if role, _, _, _, _, _ := lm([]byte(ls[i])); role != "" {
			return role
		}
	}
	return ""
}

// tailBytes returns up to the last n bytes of a file (best-effort).
func tailBytes(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if fi.Size() > n {
		_, _ = f.Seek(fi.Size()-n, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	return b
}

// hashToPath reverses gemini's projectHash (= sha256 of the project cwd) by
// hashing every directory we plausibly know about: the cwds recorded by other
// tools' sessions, ~, and ~/dev/*. Best-effort — an unmatched hash just means
// the location stays unknown.
func hashToPath(home string, known []aiSession) map[string]string {
	cands := map[string]bool{home: true}
	for _, s := range known {
		if s.Cwd != "" {
			cands[s.Cwd] = true
		}
	}
	if entries, err := os.ReadDir(filepath.Join(home, "dev")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				cands[filepath.Join(home, "dev", e.Name())] = true
			}
		}
	}
	m := make(map[string]string, len(cands))
	for p := range cands {
		m[fmt.Sprintf("%x", sha256.Sum256([]byte(p)))] = p
	}
	return m
}

func aiList(args []string) {
	showAll := false
	for _, a := range args {
		if a == "--all" || a == "-a" {
			showAll = true
		}
	}
	ss := collectSessions()
	hidden := 0
	var view []aiSession
	for _, s := range ss {
		if !showAll && isAgentSession(s) {
			hidden++
			continue
		}
		view = append(view, s)
	}
	if len(view) == 0 {
		fmt.Println("no AI CLI sessions found (looked for claude, codex, gemini, antigravity, llm)")
		return
	}
	// Columns: tool · age · id(short) · project · title. Width-bounded so a long
	// title or deep path never blows up the terminal.
	fmt.Printf("%-7s  %-6s  %-9s  %-18s  %s\n", "TOOL", "AGE", "ID", "PROJECT", "TITLE")
	for _, s := range view {
		proj := "—"
		if s.Cwd != "" {
			proj = filepath.Base(s.Cwd)
		}
		tag := ""
		if s.resumeArgv == nil {
			tag = " (list-only)"
		}
		fmt.Printf("%-7s  %-6s  %-9s  %-18s  %s%s\n",
			toolLabel(s.Tool), humanAge(s.LastActive), short(s.ID, 8), trunc(proj, 18), trunc(s.Title, 56), tag)
	}
	if hidden > 0 {
		fmt.Printf("\n  %d agent/automated session(s) hidden — `ptln llms ls --all` to show\n", hidden)
	}
	if _, err := exec.LookPath("llm"); err != nil {
		fmt.Println("  llm: install `llm` to index its conversations (brew install llm)")
	}
}

// isAgentSession reports sessions that were spawned programmatically (claude-mem
// observers, partyline party agents, one-shot background workers) rather than
// started by a human at a prompt. You'd never hand-resume these, and on this
// machine they outnumber real sessions ~15:1, so they're hidden by default.
// Signature: a synthetic project label, or a first message that is a role/system
// prompt ("You are …", "Hello memory agent", anything mentioning Claude-Mem).
func isAgentSession(s aiSession) bool {
	if filepath.Base(s.Cwd) == "observer-sessions" {
		return true
	}
	t := s.Title
	switch {
	case strings.HasPrefix(t, "You are "):
		return true
	case strings.HasPrefix(t, "Hello memory agent"):
		return true
	case strings.Contains(t, "Claude-Mem"):
		return true
	}
	return false
}

func aiResume(idArg string) {
	ss := collectSessions()
	var matches []aiSession
	for _, s := range ss {
		if s.ID == idArg || strings.HasPrefix(s.ID, idArg) {
			matches = append(matches, s)
		}
	}
	switch {
	case len(matches) == 0:
		fatal(fmt.Errorf("no session matches %q — run `ptln llms` to see ids", idArg))
	case len(matches) > 1:
		fmt.Fprintf(os.Stderr, "%q matches %d sessions — be more specific:\n", idArg, len(matches))
		for _, s := range matches {
			fmt.Fprintf(os.Stderr, "  %-9s %-7s %s\n", short(s.ID, 8), s.Tool, trunc(s.Title, 50))
		}
		os.Exit(2)
	}
	resumeSession(matches[0])
}

// resumeSession chdir's to the session's recorded dir and replaces this process
// with the tool's native resume command. Shared by `llms resume` and the menu.
func resumeSession(s aiSession) {
	if s.resumeArgv == nil {
		// Gemini records only a project hash, not a path, so we can't chdir to the
		// right project to resume by index. Be honest rather than guess wrong.
		fatal(fmt.Errorf("%s sessions are list-only here (no recorded path to resume into).\n"+
			"  Resume natively: cd <the project> && %s --resume", s.Tool, s.Tool))
	}
	if s.resumeDir != "" {
		if err := os.Chdir(s.resumeDir); err != nil {
			fmt.Fprintf(os.Stderr, "ptln llms: can't cd to %s (%v) — resuming in current dir\n", s.resumeDir, err)
		}
	}
	bin, err := exec.LookPath(s.resumeArgv[0])
	if err != nil {
		fatal(fmt.Errorf("%s not found on PATH — is it installed?", s.resumeArgv[0]))
	}
	fmt.Printf("↻ resuming %s session %s\n", s.Tool, short(s.ID, 8))
	// Hand the terminal over entirely — replace this process with the tool's resume.
	if err := syscall.Exec(bin, s.resumeArgv, os.Environ()); err != nil {
		fatal(fmt.Errorf("exec %s: %w", s.resumeArgv[0], err))
	}
}

// ---- adapters -------------------------------------------------------------

// Claude Code: ~/.claude/projects/<encoded-cwd>/<sessionId>.jsonl
// Each line is a JSON event; lines carry `cwd`/`sessionId`/`type`; the first
// type:"user" line gives the title. Resume: `claude --resume <id>` in cwd.
func claudeSessions(home string) []aiSession {
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
	var out []aiSession
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		e := cachedScan(f, fi.Size(), func() scanEntry {
			cwd, title := scanClaude(f)
			return scanEntry{Cwd: cwd, Title: title}
		})
		out = append(out, aiSession{
			Tool: "claude", ID: id, Cwd: e.Cwd, Title: e.Title, LastActive: fi.ModTime(),
			storePath: f, resumeDir: e.Cwd, resumeArgv: []string{"claude", "--resume", id},
		})
	}
	return out
}

func scanClaude(path string) (cwd, title string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	// cwd + the first user message live in the early lines. Read only a bounded PREFIX and
	// skip oversized lines, so a huge session file (100s of MB of embedded tool output) can't
	// make this crawl — `ptln llms` scans EVERY session, so one giant file must stay cheap.
	buf := make([]byte, 1<<20) // 1MB prefix
	n, _ := io.ReadFull(f, buf)
	for _, line := range bytes.Split(buf[:n], []byte{'\n'}) {
		if len(line) == 0 || len(line) > 256<<10 { // skip blanks + embedded blobs
			continue
		}
		var ev struct {
			Type    string          `json:"type"`
			Cwd     string          `json:"cwd"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Cwd != "" && cwd == "" {
			cwd = ev.Cwd
		}
		if title == "" && ev.Type == "user" {
			if t := firstUserText(ev.Message); t != "" {
				title = t
			}
		}
		if cwd != "" && title != "" {
			break
		}
	}
	return
}

// Codex: ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
// Line 1 is type:"session_meta" with payload {id, cwd, timestamp}; later
// response_item/event_msg lines carry the conversation. Resume: `codex resume <id>`.
func codexSessions(home string) []aiSession {
	root := filepath.Join(home, ".codex", "sessions")
	var out []aiSession
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return nil
		}
		ent := cachedScan(p, fi.Size(), func() scanEntry {
			id, cwd, title := scanCodex(p)
			return scanEntry{ID: id, Cwd: cwd, Title: title}
		})
		if ent.ID == "" {
			return nil
		}
		out = append(out, aiSession{
			Tool: "codex", ID: ent.ID, Cwd: ent.Cwd, Title: ent.Title, LastActive: fi.ModTime(),
			storePath: p, resumeDir: ent.Cwd, resumeArgv: []string{"codex", "resume", ent.ID},
		})
		return nil
	})
	return out
}

func scanCodex(path string) (id, cwd, title string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for n := 0; sc.Scan() && n < 80; n++ {
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				ID      string          `json:"id"`
				Cwd     string          `json:"cwd"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
				Message string          `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type == "session_meta" {
			id, cwd = ev.Payload.ID, ev.Payload.Cwd
		}
		if title == "" && ev.Payload.Role == "user" {
			if t := firstUserText(ev.Payload.Content); t != "" {
				title = t
			}
		}
		if id != "" && title != "" {
			break
		}
	}
	return
}

// Gemini: ~/.gemini/tmp/<projectHash>/chats/session-*.json
// = {sessionId, projectHash, lastUpdated, messages:[{role/type, text/content}]}.
// projectHash = sha256(cwd), which we reverse via hashes (best-effort). When the
// cwd is recovered we offer `gemini --resume <sessionId>` there (current gemini
// accepts a session id — its own quit summary prints exactly that command);
// sessions whose hash we can't reverse stay list-only.
func geminiSessions(home string, hashes map[string]string) []aiSession {
	files, _ := filepath.Glob(filepath.Join(home, ".gemini", "tmp", "*", "chats", "session-*.json"))
	var out []aiSession
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			SessionID   string `json:"sessionId"`
			ProjectHash string `json:"projectHash"`
			LastUpdated string `json:"lastUpdated"`
			Messages    []struct {
				Role string `json:"role"`
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"messages"`
		}
		if json.Unmarshal(b, &s) != nil || s.SessionID == "" {
			continue
		}
		title := ""
		for _, m := range s.Messages {
			if (m.Role == "user" || m.Type == "user") && strings.TrimSpace(m.Text) != "" {
				title = cleanTitle(m.Text)
				break
			}
		}
		last := parseTime(s.LastUpdated)
		if last.IsZero() {
			if fi, e := os.Stat(f); e == nil {
				last = fi.ModTime()
			}
		}
		gs := aiSession{Tool: "gemini", ID: s.SessionID, Cwd: hashes[s.ProjectHash], Title: title, LastActive: last, storePath: f}
		if gs.Cwd != "" {
			gs.resumeDir = gs.Cwd
			gs.resumeArgv = []string{"gemini", "--resume", gs.ID}
		}
		out = append(out, gs)
	}
	return out
}

// Google Antigravity (`agy`): the agentic terminal CLI stores each conversation as a
// SQLite db under ~/.gemini/antigravity-cli/conversations/<id>.db (the filename id is
// the resume handle), with a plaintext JSONL transcript under brain/<id>/. We enumerate
// the dbs, title from the transcript's first user turn, and map the cwd via
// cache/last_conversations.json (cwd → latest id). Resume: `agy --conversation <id>`.
func antigravitySessions(home string) []aiSession {
	base := filepath.Join(home, ".gemini", "antigravity-cli")
	dbs, _ := filepath.Glob(filepath.Join(base, "conversations", "*.db"))
	if len(dbs) == 0 {
		return nil
	}
	// last_conversations.json maps cwd → id (latest per dir); invert to id → cwd so we
	// can show the project for the conversations it covers.
	idDir := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(base, "cache", "last_conversations.json")); err == nil {
		var m map[string]string
		if json.Unmarshal(b, &m) == nil {
			for dir, id := range m {
				idDir[id] = dir
			}
		}
	}
	var out []aiSession
	for _, db := range dbs {
		id := strings.TrimSuffix(filepath.Base(db), ".db")
		last := time.Time{}
		if fi, e := os.Stat(db); e == nil {
			last = fi.ModTime() // the db is rewritten on every turn → tracks "live"
		}
		title, tpath := antigravityTitle(base, id)
		out = append(out, aiSession{
			Tool: "antigravity", ID: id, Cwd: idDir[id], Title: title, LastActive: last,
			storePath: tpath, resumeDir: idDir[id], resumeArgv: []string{"agy", "--conversation", id},
		})
	}
	return out
}

// antigravityTitle reads a conversation's JSONL transcript and returns its first user
// request (unwrapped from the <USER_REQUEST> envelope) + the transcript path. Empty if
// there's no transcript yet (a brand-new conversation).
func antigravityTitle(base, id string) (title, path string) {
	path = filepath.Join(base, "brain", id, ".system_generated", "logs", "transcript.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // transcript lines carry full turns — can be big
	for sc.Scan() {
		var ln struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(sc.Bytes(), &ln) != nil || ln.Type != "USER_INPUT" {
			continue
		}
		return cleanTitle(unwrapUserRequest(ln.Content)), path
	}
	return "", path
}

// unwrapUserRequest extracts the text from antigravity's <USER_REQUEST>…</USER_REQUEST>
// envelope (the rest of the turn is metadata/settings noise); raw content as a fallback.
func unwrapUserRequest(s string) string {
	const open, end = "<USER_REQUEST>", "</USER_REQUEST>"
	if i := strings.Index(s, open); i >= 0 {
		if j := strings.Index(s[i+len(open):], end); j >= 0 {
			return strings.TrimSpace(s[i+len(open) : i+len(open)+j])
		}
	}
	return s
}

// Groq: there's no groq agentic CLI — the conventional path is simonw's `llm`
// (`llm -m groq/… -c`), which logs conversations. If `llm` is on PATH we read its
// log as JSON; otherwise this is a no-op and aiList prints an install hint.
func llmSessions() []aiSession {
	if _, err := exec.LookPath("llm"); err != nil {
		return nil
	}
	out, err := exec.Command("llm", "logs", "list", "--json", "-n", "100").Output()
	if err != nil {
		return nil
	}
	var rows []struct {
		ConversationID   string `json:"conversation_id"`
		ConversationName string `json:"conversation_name"`
		Prompt           string `json:"prompt"`
		Model            string `json:"model"`
		Datetime         string `json:"datetime_utc"`
	}
	if json.Unmarshal(out, &rows) != nil {
		return nil
	}
	// Collapse to one entry per conversation (logs list is per-response).
	seen := map[string]bool{}
	var ss []aiSession
	for _, r := range rows {
		if r.ConversationID == "" || seen[r.ConversationID] {
			continue
		}
		seen[r.ConversationID] = true
		title := r.ConversationName
		if title == "" {
			title = cleanTitle(r.Prompt)
		}
		ss = append(ss, aiSession{
			Tool: "llm", ID: r.ConversationID, Title: title, LastActive: parseTime(r.Datetime),
			resumeArgv: []string{"llm", "chat", "-c", r.ConversationID},
		})
	}
	return ss
}

// ---- detail (lazy, for the highlighted row in the menu) -------------------

type aiDetail struct {
	Messages      int
	Size          int64 // store file size (shown when Messages isn't counted)
	Model         string
	Started       time.Time
	Ended         time.Time   // last activity timestamp (→ duration)
	Tokens        int64       // input+output tokens (claude: summed; codex: cumulative total)
	TokensPartial bool        // true when a bounded read couldn't sum the whole transcript
	First         string      // first user prompt
	Last          string      // last message text
	Recent        []recentMsg // the last few messages (role + text) for the preview tail
	Branch        string      // git branch at the recorded cwd, if any
	Dirty         int         // uncommitted changes in the cwd right now (-1 = unknown)
	Gone          bool        // recorded cwd no longer exists
	Memory        []ctxFile   // agent memory in scope (CLAUDE.md / AGENTS.md / GEMINI.md)
	MCP           []string    // configured MCP server names
	Skills        []string    // available skill names (Claude)
}

// recentMsg is one entry in the detail pane's recent-conversation tail.
type recentMsg struct {
	Role string
	Text string
}

const maxRecentMsgs = 5

// pushRecent appends a message to the recent-tail ring buffer, capping each message's
// stored length and keeping only the last maxRecentMsgs.
func (d *aiDetail) pushRecent(role, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > 800 {
		text = text[:800] + "…"
	}
	d.Recent = append(d.Recent, recentMsg{Role: role, Text: text})
	if len(d.Recent) > maxRecentMsgs {
		d.Recent = d.Recent[len(d.Recent)-maxRecentMsgs:]
	}
}

var detailCache = map[string]*aiDetail{}

// detailFor reads the full session store for one session to surface metadata +
// a content preview. Cached per id so re-selecting a row is instant. Only ever
// called for the row the cursor is on, so reading whole files stays cheap.
// sessionModel reads a session's model for the mux picker (cached via detailFor). Called
// once per session at open time, not per render.
func sessionModel(s aiSession) string { return detailFor(s).Model }

func detailFor(s aiSession) *aiDetail {
	if d, ok := detailCache[s.ID]; ok {
		return d
	}
	d := &aiDetail{First: s.Title, Started: s.LastActive, Dirty: -1}
	switch s.Tool {
	case "claude":
		readJSONLDetail(s, d, claudeLineMsg)
	case "codex":
		readJSONLDetail(s, d, codexLineMsg)
	case "gemini":
		geminiDetail(s, d)
	}
	if s.Cwd != "" {
		if _, err := os.Stat(s.Cwd); err != nil {
			d.Gone = true
		} else if out, err := exec.Command("git", "-C", s.Cwd, "branch", "--show-current").Output(); err == nil {
			d.Branch = strings.TrimSpace(string(out))
			// Uncommitted-change count in the cwd right now — "is this session's
			// work saved?" One cheap porcelain call; -1 stays if it's not a repo.
			if st, e := exec.Command("git", "-C", s.Cwd, "status", "--porcelain").Output(); e == nil {
				d.Dirty = 0
				for _, b := range st {
					if b == '\n' {
						d.Dirty++
					}
				}
			}
		}
	}
	loadContext(s, d)
	detailCache[s.ID] = d
	return d
}

// ---- session context inspection (memory file · MCP servers · skills) ----------
// A read-only surface of what an agent session runs *with*. Best-effort: a missing
// or malformed file just yields nothing. Cross-tool where it makes sense — the
// memory file is CLAUDE.md / AGENTS.md / GEMINI.md per tool; skills are Claude-only.
type ctxFile struct {
	Path  string
	Scope string // "project" | "global"
	Lines int
}

func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }

func memoryFileName(tool string) string {
	switch tool {
	case "claude":
		return "CLAUDE.md"
	case "codex":
		return "AGENTS.md"
	case "gemini":
		return "GEMINI.md"
	}
	return ""
}

func countLines(p string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n := strings.Count(string(b), "\n")
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		n++
	}
	return n
}

func mcpKeysFromJSON(b []byte) []string {
	var v struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	out := make([]string, 0, len(v.McpServers))
	for k := range v.McpServers {
		out = append(out, k)
	}
	return out
}

// skillNames lists skills in a skills dir: a subdir with a SKILL.md, or a bare *.md.
func skillNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			if fileExists(filepath.Join(dir, e.Name(), "SKILL.md")) {
				out = append(out, e.Name())
			}
		} else if strings.HasSuffix(e.Name(), ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	return out
}

func loadContext(s aiSession, d *aiDetail) {
	home, _ := os.UserHomeDir()

	// Memory file: project (cwd) + global, named per tool.
	if name := memoryFileName(s.Tool); name != "" {
		if s.Cwd != "" {
			if p := filepath.Join(s.Cwd, name); fileExists(p) {
				d.Memory = append(d.Memory, ctxFile{p, "project", countLines(p)})
			}
		}
		global := map[string]string{
			"claude": filepath.Join(home, ".claude", "CLAUDE.md"),
			"codex":  filepath.Join(home, ".codex", "AGENTS.md"),
			"gemini": filepath.Join(home, ".gemini", "GEMINI.md"),
		}[s.Tool]
		if global != "" && fileExists(global) {
			d.Memory = append(d.Memory, ctxFile{global, "global", countLines(global)})
		}
	}

	// MCP servers: project .mcp.json (tool-agnostic) + Claude's global/per-project config.
	seen := map[string]bool{}
	addMCP := func(names []string) {
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				d.MCP = append(d.MCP, n)
			}
		}
	}
	if s.Cwd != "" {
		if b, err := os.ReadFile(filepath.Join(s.Cwd, ".mcp.json")); err == nil {
			addMCP(mcpKeysFromJSON(b))
		}
	}
	if s.Tool == "claude" {
		if b, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
			var v struct {
				McpServers map[string]json.RawMessage `json:"mcpServers"`
				Projects   map[string]struct {
					McpServers map[string]json.RawMessage `json:"mcpServers"`
				} `json:"projects"`
			}
			if json.Unmarshal(b, &v) == nil {
				for k := range v.McpServers {
					addMCP([]string{k})
				}
				if s.Cwd != "" {
					if p, ok := v.Projects[s.Cwd]; ok {
						for k := range p.McpServers {
							addMCP([]string{k})
						}
					}
				}
			}
		}
	}
	sort.Strings(d.MCP)

	// Skills (Claude): user + project, deduped.
	if s.Tool == "claude" {
		seenS := map[string]bool{}
		dirs := []string{filepath.Join(home, ".claude", "skills")}
		if s.Cwd != "" {
			dirs = append(dirs, filepath.Join(s.Cwd, ".claude", "skills"))
		}
		for _, dir := range dirs {
			for _, n := range skillNames(dir) {
				if !seenS[n] {
					seenS[n] = true
					d.Skills = append(d.Skills, n)
				}
			}
		}
		sort.Strings(d.Skills)
	}
}

// a lineMsg extracts one jsonl event. role=="" means "not a message" (e.g. a
// token-count line) — its toks still count. cumulative=true means toks is a
// running total (codex), so we take the max rather than summing.
type lineMsg func([]byte) (role, text, model string, ts time.Time, toks int64, cumulative bool)

// readJSONLDetail must stay cheap — it runs synchronously on cursor moves, and
// session transcripts can be 100MB+. Full parse only under fullParseMax; bigger
// files get a bounded HEAD read (first prompt / model / start time) plus a
// bounded TAIL read (latest message), and report size instead of a message count.
const fullParseMax = 2 << 20   // 2MB
const boundedChunk = 256 << 10 // 256KB head/tail window

func readJSONLDetail(s aiSession, d *aiDetail, parse lineMsg) {
	path := s.storePath
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	d.Size = fi.Size()
	full := d.Size <= fullParseMax

	sawSummed := false // any per-turn (non-cumulative) tokens → a bounded read undercounts
	addToks := func(toks int64, cumulative bool) {
		if toks <= 0 {
			return
		}
		if cumulative {
			if toks > d.Tokens {
				d.Tokens = toks
			}
		} else {
			d.Tokens += toks
			sawSummed = true
		}
	}
	scanPart := func(sc *bufio.Scanner, maxLines int) {
		firstSet := d.First != "" && d.Messages > 0
		for n := 0; sc.Scan() && (maxLines == 0 || n < maxLines); n++ {
			role, text, model, ts, toks, cumulative := parse(sc.Bytes())
			addToks(toks, cumulative)
			if !ts.IsZero() {
				if d.Started.IsZero() || ts.Before(d.Started) {
					d.Started = ts
				}
				if ts.After(d.Ended) {
					d.Ended = ts
				}
			}
			if role == "" {
				continue
			}
			d.Messages++
			if model != "" {
				d.Model = model
			}
			if text == "" {
				continue
			}
			// First prompt: skip meta-turns (<environment_context>, <command-…>)
			// so the preview shows what the human actually asked; extractRequest
			// unwraps templated prompts (claude-mem etc.) to the real ask.
			if role == "user" && !firstSet && !strings.HasPrefix(text, "<") {
				d.First, firstSet = extractRequest(text), true
			}
			d.Last = text
			d.pushRecent(role, text)
		}
	}

	if full {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		scanPart(sc, 0)
		return
	}
	// HEAD: meta + first prompt live in the first lines.
	head := bufio.NewScanner(io.LimitReader(f, boundedChunk))
	head.Buffer(make([]byte, 1<<20), 1<<24)
	scanPart(head, 200)
	// TAIL: seek near the end, drop the partial first line, take the rest. This
	// captures the latest message AND codex's cumulative token total (last line).
	if _, err := f.Seek(d.Size-boundedChunk, io.SeekStart); err == nil {
		tail := bufio.NewScanner(f)
		tail.Buffer(make([]byte, 1<<20), 1<<24)
		tail.Scan()             // discard the partial line we landed inside
		d.Recent = d.Recent[:0] // the genuine recent messages live in the tail, not the head
		for tail.Scan() {
			role, text, _, ts, toks, cumulative := parse(tail.Bytes())
			addToks(toks, cumulative)
			if !ts.IsZero() && ts.After(d.Ended) {
				d.Ended = ts
			}
			if role != "" && text != "" {
				d.Last = text
				d.pushRecent(role, text)
			}
		}
	}
	d.Messages = 0              // unknown without a full parse — the UI shows Size instead
	d.TokensPartial = sawSummed // codex (cumulative) stays accurate; claude (summed) is partial
}

func claudeLineMsg(b []byte) (role, text, model string, ts time.Time, toks int64, cumulative bool) {
	var ev struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Role    string          `json:"role"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &ev) != nil || (ev.Type != "user" && ev.Type != "assistant") {
		return
	}
	// Per-turn usage on assistant lines → summed (not cumulative).
	toks = ev.Message.Usage.Input + ev.Message.Usage.Output
	return ev.Type, contentText(ev.Message.Content), ev.Message.Model, parseTime(ev.Timestamp), toks, false
}

func codexLineMsg(b []byte) (role, text, model string, ts time.Time, toks int64, cumulative bool) {
	var ev struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Model   string          `json:"model"`
			Info    struct {
				Total struct {
					Total int64 `json:"total_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return
	}
	ts = parseTime(ev.Timestamp)
	// token_count events carry a CUMULATIVE running total (take the max/last).
	if ev.Type == "event_msg" && ev.Payload.Type == "token_count" {
		return "", "", "", ts, ev.Payload.Info.Total.Total, true
	}
	if ev.Type != "response_item" || ev.Payload.Role == "" {
		return "", "", "", ts, 0, false
	}
	return ev.Payload.Role, contentText(ev.Payload.Content), ev.Payload.Model, ts, 0, false
}

func geminiDetail(s aiSession, d *aiDetail) {
	if s.storePath == "" {
		return
	}
	b, err := os.ReadFile(s.storePath)
	if err != nil {
		return
	}
	var g struct {
		Messages []struct {
			Role string `json:"role"`
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if json.Unmarshal(b, &g) != nil {
		return
	}
	for _, m := range g.Messages {
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		d.Messages++
		d.Last = cleanTitle(m.Text)
		d.pushRecent(m.Role, m.Text)
	}
}

// contentText flattens a message `content` (string or []{text}) to plain text.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return cleanLine(str)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if strings.TrimSpace(b.Text) != "" {
				return cleanLine(b.Text)
			}
		}
	}
	return ""
}

// cleanLine one-lines text for preview without stripping role-prompt prefixes
// (detail preview shows real content; the agent-filter already ran on Title).
func cleanLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- helpers --------------------------------------------------------------

// firstUserText pulls the text out of a message whose `content` is either a
// plain string or an array of {type,text} blocks (both shapes occur).
func firstUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// content may be at .content (claude) or be the raw value itself (codex payload.content)
	var withContent struct {
		Content json.RawMessage `json:"content"`
	}
	c := raw
	if json.Unmarshal(raw, &withContent) == nil && len(withContent.Content) > 0 {
		c = withContent.Content
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return cleanTitle(s)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(c, &blocks) == nil {
		for _, b := range blocks {
			if strings.TrimSpace(b.Text) != "" {
				return cleanTitle(b.Text)
			}
		}
	}
	return ""
}

// cleanTitle strips the "[name]: " prefix our shell adds, drops command/system
// meta turns (which start with "<"), collapses whitespace, and one-lines it.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	s = extractRequest(s) // pull the real ask out of templated wrappers first
	if i := strings.Index(s, "]: "); i >= 0 && i < 24 && strings.HasPrefix(s, "[") {
		s = s[i+3:]
	}
	if strings.HasPrefix(s, "<") { // <command-name>, <system-reminder>, etc.
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}

// extractRequest surfaces the human ask buried inside templated prompts. Tools
// (claude-mem observers, agent runners) wrap the real request in identical
// boilerplate — e.g. "Hello memory agent … <user_request>WHAT THEY ASKED
// </user_request>". Without this every such session shows the same useless
// preamble; with it each shows what it was actually about. No-op for normal
// prompts (no wrapper tag → returned unchanged).
func extractRequest(s string) string {
	for _, tag := range []string{"user_request", "user-request", "request", "task"} {
		open := "<" + tag + ">"
		if i := strings.Index(s, open); i >= 0 {
			inner := s[i+len(open):]
			if j := strings.Index(inner, "</"+tag+">"); j >= 0 {
				inner = inner[:j]
			}
			if t := strings.TrimSpace(inner); t != "" {
				return t
			}
		}
	}
	return s
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func humanDur(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// trunc bounds s to n display columns (emoji/CJK count as 2), with an ellipsis.
func trunc(s string, n int) string {
	if runewidth.StringWidth(s) > n {
		return runewidth.Truncate(s, n-1, "") + "…"
	}
	return s
}
