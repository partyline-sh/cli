package main

// runLauncherEmpty hosts the existing empty-state form and welcome screen in this popup.
func runLauncherEmpty() error {
	if offerSessionRoots() {
		all := collectSessions()
		saveLauncherCache(all)
		if len(all) > 0 {
			return runLauncherPopup()
		}
	}
	doors := []welcomeDoor{{disp: "n", key: 'n', title: "new session", run: func() error {
		spec := *shellSpec()
		target, err := tmuxMerge([]Spec{spec})
		if err != nil {
			return err
		}
		return tmuxFocus(target)
	}}}
	idx, err := welcomeLoop(doors, false)
	if err != nil || idx < 0 {
		return err
	}
	return doors[idx].run()
}
