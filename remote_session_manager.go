package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// The phone sends identities only. Commands and directories always come from the
// host's launcher index, never from client-supplied argv or filesystem paths.
type remoteSavedSession struct {
	Key         string `json:"key"`
	Engine      string `json:"engine"`
	Title       string `json:"title"`
	Project     string `json:"project"`
	UpdatedAt   string `json:"updatedAt"`
	Pane        string `json:"pane,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}
type remoteSavedPage struct {
	Sessions   []remoteSavedSession `json:"sessions"`
	Total      int                  `json:"total"`
	NextOffset *int                 `json:"nextOffset,omitempty"`
}

var remoteSessionPane = regexp.MustCompile(`^%[0-9]+$`)

func remoteSessionControl(phone string, m remote.Message, now time.Time) (string, string, any) {
	var req struct {
		Query  string `json:"query"`
		Offset int    `json:"offset"`
		Engine string `json:"engine"`
		Key    string `json:"key"`
	}
	raw, err := json.Marshal(m.Data)
	if err != nil || len(raw) > 2048 || json.Unmarshal(raw, &req) != nil || len(req.Query) > 256 || len(req.Key) > 256 || len(req.Engine) > 32 {
		return "refused", "Invalid session request", nil
	}
	if m.Type == "session_close" {
		// A stale phone may still show a pane that was closed/replaced. Verify the
		// stable session key immediately before killing only that exact pane.
		sp, ok := remoteManagedSession(m.Pane, req.Key)
		if !ok {
			return "refused", "Session changed or is no longer open; refresh the sessions list", nil
		}
		if err = remoteControlAudit(phone, "session_close", sp.Key, "", "", now); err == nil {
			err = tmuxCmd("kill-pane", "-t", m.Pane).Run()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "remote session close failed:", err)
			return "refused", "Could not close session; refresh and try again", nil
		}
		saveWorkspace(tmuxWorkspaceSpecs())
		return "ok", "", map[string]string{"pane": m.Pane, "key": sp.Key}
	}
	if m.Pane != "" {
		return "refused", "No pane accepted for this request", nil
	}
	all, _, ok := loadLauncherSnapshot()
	if !ok {
		return "refused", "Session index is still loading on this computer. Try again shortly.", nil
	}
	meta := loadLLMMeta()
	applyCwdOverrides(all, meta)
	panes := remoteSessionPanes()
	if m.Type == "session_list" {
		if req.Offset < 0 {
			return "refused", "Invalid session offset", nil
		}
		page := remoteSavedPage{Sessions: []remoteSavedSession{}}
		q := strings.ToLower(strings.TrimSpace(req.Query))
		for _, s := range all {
			if isAgentSession(s) || s.Tool == "terminal" {
				continue
			}
			title := meta[s.ID].Name
			if title == "" {
				title = s.Title
			}
			if title == "" {
				title = s.ID
			}
			if !strings.Contains(strings.ToLower(title+" "+s.Cwd+" "+s.Tool), q) {
				continue
			}
			i := page.Total
			page.Total++
			if i < req.Offset || len(page.Sessions) >= 30 {
				continue
			}
			row := remoteSavedSession{Key: s.ID, Engine: s.Tool, Title: remoteSessionText(title, 160), Project: remoteSessionText(s.Cwd, 240), UpdatedAt: s.LastActive.UTC().Format(time.RFC3339), Pane: panes[s.ID]}
			if row.Pane == "" {
				if e := remoteSessionReady(s); e != nil {
					row.Unavailable = e.Error()
				}
			}
			page.Sessions = append(page.Sessions, row)
		}
		if next := req.Offset + len(page.Sessions); next < page.Total {
			page.NextOffset = &next
		}
		if err := remoteDetailSize(page); err != nil {
			return "refused", err.Error(), nil
		}
		return "ok", "", page
	}
	if m.Type != "session_open" || req.Key == "" || req.Engine == "" {
		return "refused", "Invalid session action", nil
	}
	for _, s := range all {
		if s.ID != req.Key || s.Tool != req.Engine || isAgentSession(s) {
			continue
		}
		if p := panes[s.ID]; p != "" {
			return "ok", "", map[string]string{"pane": p}
		}
		if e := remoteSessionReady(s); e != nil {
			return "refused", e.Error(), nil
		}
		if err = remoteControlAudit(phone, "session_open", s.ID, "", "", now); err != nil {
			return "refused", "Could not record session open", nil
		}
		// Detached merge deduplicates against live panes without changing the console
		// being used by another phone or desktop client.
		sp := launcherSpec(s, meta)
		pane, e := tmuxMerge([]Spec{sp})
		if e != nil {
			fmt.Fprintln(os.Stderr, "remote session open failed:", e)
			return "refused", "Could not open session; check the engine on the computer", nil
		}
		saveWorkspace(tmuxWorkspaceSpecs())
		return "ok", "", map[string]string{"pane": pane}
	}
	return "refused", "Session is no longer in the index; refresh and try again", nil
}

func remoteSessionText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func remoteSessionReady(s aiSession) error {
	if len(s.resumeArgv) == 0 {
		return errors.New("This engine does not support resuming")
	}
	home, _ := os.UserHomeDir()
	if s.root != "" && s.root != home {
		return errors.New("Open sessions from another home directory on the computer")
	}
	if s.storePath != "" {
		if _, err := os.Stat(s.storePath); err != nil {
			return errors.New("Saved conversation is no longer available; refresh the index")
		}
	}
	if s.resumeDir == "" {
		return errors.New("Session has no working directory")
	}
	if fi, e := os.Stat(s.resumeDir); e != nil || !fi.IsDir() {
		return errors.New("Working directory is missing; recover it on the computer")
	}
	if _, e := exec.LookPath(s.resumeArgv[0]); e != nil {
		return errors.New("Install this engine on the computer to resume")
	}
	return nil
}
func remoteSessionPanes() map[string]string {
	panes := map[string]string{}
	out, err := tmuxCmd("list-panes", "-s", "-t", "="+tmuxSessionName, "-F", "#{pane_id}\t#{@ptln_key}").Output()
	if err != nil {
		return panes
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 && f[1] != "" && remoteSessionPane.MatchString(f[0]) {
			panes[f[1]] = f[0]
		}
	}
	return panes
}
func remotePaneForKey(key string) string { return remoteSessionPanes()[key] }
func remoteManagedSession(pane, key string) (Spec, bool) {
	if !remoteSessionPane.MatchString(pane) || key == "" {
		return Spec{}, false
	}
	if remotePaneForKey(key) != pane {
		return Spec{}, false
	}
	sp, ok := paneSpec(pane)
	return sp, ok && sp.Key == key
}
