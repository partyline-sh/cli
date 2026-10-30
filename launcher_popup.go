package main

import tea "charm.land/bubbletea/v2"

// launcherSessions delivers a fresh scan after the cached first frame.
type launcherSessions struct {
	all  []aiSession
	live map[string]bool
}

// launcherPopup holds the existing launcher's tree and its deferred pick.
type launcherPopup struct {
	menu     *aiMenu
	scan     func() []aiSession
	live     func() map[string]bool
	picked   *aiSession
	action   string
	sessions []aiSession
	back     bool
}

// newLauncherPopup paints a cached snapshot, scanning synchronously only on a cache miss.
func newLauncherPopup(scan func() []aiSession) launcherPopup {
	all, live, ok := loadLauncherSnapshot()
	// An empty snapshot is scanned too: it would otherwise send you to onboarding before the
	// refresh that would have found sessions started since.
	if !ok || len(all) == 0 {
		all = scan()
	}
	m := &aiMenu{all: all, meta: loadLLMMeta(), live: live, w: 80, h: 24, updateLine: updateAvailable()}
	m.applyFilter()
	return launcherPopup{menu: m, scan: scan}
}

// Init refreshes the session snapshot without delaying the cached frame.
func (m launcherPopup) Init() tea.Cmd {
	live := m.menu.live
	return func() tea.Msg {
		all := m.scan()
		if m.live != nil {
			live = m.live()
		}
		// Kept for the next open, so only the first open after a reboot waits for a scan, even
		// before the watcher has run.
		saveLauncherCache(all, live)
		return launcherSessions{all: all, live: live}
	}
}

// Update refreshes the shared model and routes keys through its host adapter.
func (m launcherPopup) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	a := m.menu
	switch msg := msg.(type) {
	case launcherPagerDone:
		if msg.err != nil {
			a.flash = msg.err.Error()
		}
	case tea.WindowSizeMsg:
		a.w, a.h = max(msg.Width, 1), max(msg.Height, 6)
		a.clampScroll()
	case launcherSessions:
		id := ""
		if s := a.selected(); s != nil {
			id = s.ID
		}
		a.all, a.live = msg.all, msg.live
		a.applyFilter()
		if len(a.all) == 0 {
			return m, tea.Quit
		}
		for i, r := range a.rows {
			if !r.header() && a.view[r.sessIdx].ID == id {
				a.cursor = i
				break
			}
		}
		a.clampScroll()
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// runLauncherPopup restores the terminal before focusing or recovering a pick.
func runLauncherPopup() error {
	m := newLauncherPopup(collectSessions)
	m.live = launcherLiveKeys
	m.menu.filter = launcherSearch
	if len(m.menu.all) == 0 {
		return runLauncherEmpty()
	}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return err
	}
	result := final.(launcherPopup)
	if len(result.menu.all) == 0 {
		return runLauncherEmpty()
	}
	return result.finish()
}
