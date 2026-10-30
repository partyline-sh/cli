package main

import (
	"os"
	"strings"
)

// Launcher host operations are replaceable so action tests never contact tmux.
var (
	launcherMerge    = tmuxMerge
	launcherFocus    = tmuxFocus
	launcherOpen     = launcherFocusSession
	launcherQuickNew = tmuxNewWindow
	launcherNewMenu  = func() { t := newTmuxSessionTarget(); newRunMenu(t); t.drain() }
	launcherRecover  = func(s aiSession, repo string, resume func(aiSession)) { recoverWithResume(s, repo, resume)() }
	launcherSplit    = launcherSplitSessions
	launcherPick     = func(all []aiSession) ([]aiSession, bool) {
		var result []aiSession
		for _, label := range []string{"left session", "right session"} {
			choices := []aiSession{}
			for _, s := range all {
				if s.resumeArgv != nil && (len(result) == 0 || s.ID != result[0].ID) {
					choices = append(choices, s)
				}
			}
			if len(choices) == 0 {
				return nil, false
			}
			i, ok := Pick(label, choices, func(s aiSession) string { return toolLabel(s.Tool) + " · " + s.Title })
			if !ok {
				return nil, false
			}
			result = append(result, choices[i])
		}
		return result, true
	}
)

// launcherReopen opens the launcher again (after a cancelled recovery). Set in init: it calls back
// into runLauncherPopup, which a package-level initialiser cannot.
var launcherReopen func() error

func init() { launcherReopen = runLauncherPopup }

// getLauncherPager uses the same default and argument splitting as the legacy viewer.
func getLauncherPager() string {
	if p := os.Getenv("PAGER"); len(strings.Fields(p)) > 0 {
		return p
	}
	return "less -R"
}

// launcherSpec preserves the metadata required for deduplication and workspace restore.
func launcherSpec(s aiSession, meta map[string]sessMeta) Spec {
	return prepareLaunchSpec(Spec{Label: muxLabelFor(s, meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir})
}

// finish applies host requests only after Bubble Tea restores the terminal.
func (m launcherPopup) finish() error {
	switch m.action {
	case "open":
		return launcherOpen(*m.picked, m.menu.meta)
	case "terminal":
		// The resumed session is tagged and saved like every other tmux tab.
		sp := launcherSpec(*m.picked, m.menu.meta)
		target, err := launcherMerge([]Spec{sp})
		if err != nil {
			return err
		}
		return launcherFocus(target)
	case "open-many":
		var specs []Spec
		for _, s := range m.sessions {
			m.menu.markUsed(s.ID)
			specs = append(specs, launcherSpec(s, m.menu.meta))
		}
		target, err := launcherMerge(specs)
		if err != nil {
			return err
		}
		return launcherFocus(target)
	case "recover":
		var err error
		resumed := false
		// The restored session opens with meta read now: recovery may have saved a relocation.
		launcherRecover(*m.picked, m.menu.meta[m.picked.ID].Repo, func(s aiSession) { resumed = true; err = launcherOpen(s, loadLLMMeta()) })
		if !resumed {
			return launcherReopen() // recovery cancelled: back to the launcher, as before
		}
		return err
	case "new":
		launcherNewMenu()
	case "quick-new":
		return launcherQuickNew(false)
	case "quick-bypass":
		return launcherQuickNew(true)
	case "split":
		sessions := m.sessions
		if len(sessions) < 2 {
			var ok bool
			sessions, ok = launcherPick(m.menu.view)
			if !ok {
				return nil
			}
		}
		return launcherSplit(sessions, m.menu.meta)
	}
	return nil
}
