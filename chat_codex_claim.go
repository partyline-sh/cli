// A new codex session finding itself in the room.
//
// `ptln new` starts every session under a launch placeholder ("new-codex-<unix nanos>"): the engine
// has not chosen its own id yet. The placeholder is set as the pane's @ptln_key and as
// PARTYLINE_SESSION_KEY, and `ptln chat add` records the member under it. Claude passes that variable
// to the commands it runs, so a new Claude finds itself. Codex cannot:
//
//   - it strips variables whose names contain KEY from its commands, so PARTYLINE_SESSION_KEY never
//     arrives;
//   - since 0.172 its commands run under the codex app-server daemon (a child of launchd), so there
//     is no tmux pane in their ancestry, and TMUX_PANE comes from a replayed shell snapshot that can
//     name a pane belonging to someone else;
//   - the one identity it does carry, CODEX_THREAD_ID, is the real thread id, which the room has
//     never heard of.
//
// So a new codex was always "not in the chat", while the waker (which goes by the pane's
// placeholder) kept waking it. The fix is a claim: each codex placeholder is paired with the codex
// thread that started in its pane's folder just after its launch, and both the member and the pane
// move to the thread id. The waker makes the claim on every pass, from outside codex's sandbox
// (which cannot reach tmux); a read makes it too when it can. From then on reads, sends and wakes
// all agree, with no manual re-adding.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// chatClaimSkew tolerates a thread whose recorded start is slightly before its launch placeholder
// (the two clocks are read by different processes). chatClaimWindow is how long after its launch a
// codex has to have started its thread: 0.172 writes the rollout as it starts, so a placeholder
// cannot be paired with a thread that began much later, which would belong to a later launch.
const (
	chatClaimSkew   = 5 * time.Second
	chatClaimWindow = 30 * time.Second
)

// codexThread is one codex rollout's identity: its id, folder and start time.
type codexThread struct {
	ID, Cwd string
	Started time.Time
}

// Variables so tests can stand in for the codex store and tmux.
var (
	chatCodexThreads = codexThreadsSince
	chatRetagPane    = retagCodexPane
)

// chatSelfOrClaim is r.self, plus the new-codex claim when nothing matches. The claim needs tmux,
// which a sandboxed codex cannot reach; for that case the waker makes the same claim from outside
// (chatClaimCodexPlaceholders), so the next read finds the member by its thread id.
func chatSelfOrClaim(r *chatRoom, keys []string) *chatMember {
	if me := r.self(keys); me != nil {
		return me
	}
	thread := strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	if thread == "" {
		return nil
	}
	if launch, ok := r.oldestCodexPlaceholder(); ok {
		r.claimCodexPlaceholders(chatPanesNow(), chatCodexThreads(launch))
	}
	return r.member(thread)
}

// chatClaimCodexPlaceholders is run by the waker on every pass, outside any sandbox.
func chatClaimCodexPlaceholders() {
	var oldest time.Time
	have := false
	_ = withChatLock(func(r *chatRoom) error {
		oldest, have = r.oldestCodexPlaceholder()
		return nil
	})
	if !have {
		return
	}
	panes, threads := chatPanesNow(), chatCodexThreads(oldest)
	_ = withChatLock(func(r *chatRoom) error {
		r.claimCodexPlaceholders(panes, threads)
		return nil
	})
}

// oldestCodexPlaceholder is the earliest launch among codex members still under a placeholder.
func (r *chatRoom) oldestCodexPlaceholder() (time.Time, bool) {
	var oldest int64
	for _, m := range r.Members {
		if at, ok := codexPlaceholderLaunch(m.Key); ok && m.Engine == "codex" && (oldest == 0 || at < oldest) {
			oldest = at
		}
	}
	return time.Unix(0, oldest), oldest != 0
}

// claimCodexPlaceholders moves each codex placeholder member onto the thread its pane started, and
// retags the pane to match. Placeholders are taken in launch order and each takes the earliest
// unclaimed thread that started in its pane's folder within chatClaimWindow of its launch.
func (r *chatRoom) claimCodexPlaceholders(panes []chatPane, threads []codexThread) {
	taken := map[string]bool{}
	for _, m := range r.Members {
		taken[m.Key] = true
	}
	for _, p := range panes {
		taken[p.Key] = true
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].Started.Before(threads[j].Started) })
	type cand struct {
		m  *chatMember
		p  chatPane
		at int64
	}
	var cands []cand
	for i := range r.Members {
		m := &r.Members[i]
		at, ok := codexPlaceholderLaunch(m.Key)
		if !ok || m.Engine != "codex" {
			continue
		}
		if p, ok := paneWithKey(panes, m.Key); ok && p.Engine == "codex" {
			cands = append(cands, cand{m, p, at})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at < cands[j].at })
	for _, c := range cands {
		launch := time.Unix(0, c.at)
		for _, t := range threads {
			if taken[t.ID] || !sameDir(c.p.Cwd, t.Cwd) || t.Started.Before(launch.Add(-chatClaimSkew)) || t.Started.After(launch.Add(chatClaimWindow)) {
				continue
			}
			if chatRetagPane(c.p.ID, t.ID) != nil {
				break // leave both as they were rather than let reads and wakes disagree
			}
			c.m.Was = append(c.m.Was, c.m.Key)
			c.m.Key, taken[t.ID] = t.ID, true
			break
		}
	}
}

// codexPlaceholderLaunch is the launch time in a "new-codex-<unix nanos>" key.
func codexPlaceholderLaunch(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, "new-codex-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil
}

// paneWithKey finds a pane by its @ptln_key only (chatPaneFor also matches tab numbers).
func paneWithKey(panes []chatPane, key string) (chatPane, bool) {
	for _, p := range panes {
		if p.Key == key {
			return p, true
		}
	}
	return chatPane{}, false
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

// codexThreadsSince lists codex rollouts written since a time, with each one's folder and start.
// Files last modified before then cannot hold a thread that started after it, so they are skipped
// without being opened.
func codexThreadsSince(since time.Time) []codexThread {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
	var out []codexThread
	for _, p := range paths {
		if fi, err := os.Stat(p); err != nil || fi.ModTime().Before(since.Add(-chatClaimSkew)) {
			continue
		}
		if t, ok := readCodexThread(p); ok && !t.Started.Before(since.Add(-chatClaimSkew)) {
			out = append(out, t)
		}
	}
	return out
}

// readCodexThread reads a rollout's first line, its session_meta.
func readCodexThread(path string) (codexThread, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexThread{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	if !sc.Scan() {
		return codexThread{}, false
	}
	var ev struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			Cwd       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != "session_meta" || ev.Payload.ID == "" {
		return codexThread{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ev.Payload.Timestamp)
	if err != nil {
		return codexThread{}, false
	}
	return codexThread{ID: ev.Payload.ID, Cwd: ev.Payload.Cwd, Started: t}, true
}

// withCodexChatDir lets a sandboxed codex use the chat. Codex's default sandbox only writes inside
// its workspace, so `ptln chat read` and `send` failed on the room's lock file under ~/.partyline.
// Only the chat directory is added: the rest of ~/.partyline holds settings and hooks that a
// sandboxed agent must not be able to rewrite. Idempotent, because some launch paths prepare a
// spec twice.
func withCodexChatDir(bin string, argv []string) []string {
	if bin != "codex" {
		return argv
	}
	dir := chatDir()
	for i, a := range argv {
		if (a == "--add-dir" && i+1 < len(argv) && argv[i+1] == dir) || a == "--add-dir="+dir {
			return argv
		}
	}
	return append(append([]string(nil), argv...), "--add-dir", dir)
}

// retagCodexPane moves a pane onto its codex thread: @ptln_key for the waker and the room, and the
// stored Spec, so a restored workspace resumes this conversation instead of starting a blank codex.
func retagCodexPane(paneID, thread string) error {
	sp, ok := paneSpec(paneID)
	if !ok {
		return tmuxCmd("set-option", "-p", "-t", paneID, "@ptln_key", thread).Run()
	}
	tagTmuxPane(paneID, claimedCodexSpec(sp, thread))
	return nil
}

// claimedCodexSpec is a new codex's Spec once its thread is known: keyed by the thread, and
// relaunched as `codex resume <thread>` with the same flags.
func claimedCodexSpec(sp Spec, thread string) Spec {
	sp.Key = thread
	if len(sp.Argv) > 0 && filepath.Base(sp.Argv[0]) == "codex" && !containsStr(sp.Argv, "resume") {
		argv := append([]string{sp.Argv[0], "resume", thread}, sp.Argv[1:]...)
		sp.Argv = argv
	}
	return sp
}
