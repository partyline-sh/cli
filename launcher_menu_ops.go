package main

// Shared launcher operations default to the legacy implementations; tests replace side effects.
var (
	launcherMenuScan    = collectSessions
	launcherMenuDelete  = deleteSession
	launcherMenuFacts   = gatherDeleteFacts
	launcherMenuBrowser = openBrowser
)
