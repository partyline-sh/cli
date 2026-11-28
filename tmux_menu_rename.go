package main

import (
	"path/filepath"
	"strings"
)

// renameMenuSession is r in the ctrl-\ menu's Sessions tab: the same saved name the launcher's r
// writes (the session's sidecar entry, which every later label reads), put on the live pane and its
// window at once. An empty name clears it back to the tool · project default.
func renameMenuSession(it tmuxMenuItem, name string) error {
	if it.sessKey != "" {
		meta := loadLLMMeta()
		if meta == nil {
			meta = map[string]sessMeta{}
		}
		mt := meta[it.sessKey]
		mt.Name = name
		meta[it.sessKey] = mt
		saveLLMMeta(meta)
	}
	sp, ok := paneSpec(it.paneID)
	if !ok {
		// An untagged pane (a shell): only its window carries a name.
		if name == "" {
			return tmuxCmd("set-option", "-w", "-t", it.paneID, "automatic-rename", "on").Run()
		}
		return tmuxCmd("rename-window", "-t", it.paneID, name).Run()
	}
	if name == "" {
		name = sp.Engine + " · " + filepath.Base(sp.Dir)
		if sp.Engine == "" || sp.Dir == "" {
			name = strings.TrimSpace(it.engine + " · " + filepath.Base(sp.Dir))
		}
	}
	sp.Label = name
	tagTmuxPane(it.paneID, sp)
	if out, err := tmuxCmd("display-message", "-p", "-t", it.paneID, "#{window_id}").Output(); err == nil {
		tmuxRenameForPanes(strings.TrimSpace(string(out)))
	}
	return nil
}
