package main

// sessIO is what injection needs from a live session, however it is hosted: write raw input
// bytes, and snapshot the visible screen (the paste-landed poll). *ptysess.Session satisfies
// it for the built-in mux; tmuxPane satisfies it for tmux-hosted sessions. This is the seam
// that lets ask-peer / ask-session delivery reach BOTH backends through one implementation —
// "one way an answer enters a session" stays true.
type sessIO interface {
	WriteInput(b []byte)
	Snapshot() []byte
	// SnapshotHistory returns up to maxLines of scrollback + screen (ask_session's answer
	// scrape). viewRows trims the live viewport for the built-in mux; tmux ignores it.
	SnapshotHistory(maxLines, viewRows int) []byte
}
