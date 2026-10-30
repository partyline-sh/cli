package main

// Spec is the spec of one session window/pane, saved in the workspace.
type Spec struct {
	Label       string   // shown in the status bar (e.g. "claude · payments")
	Key         string   // stable id (the llms session id) — dedupes "open" vs "switch"
	Model       string   // model name, shown in the session picker ("" hides it)
	Argv        []string // program + args (e.g. ["claude","--resume",id]); resumeArgv
	Dir         string   // working dir ("" inherits)
	Thread      string   // Common Ground thread this session is attached to ("" = none)
	ThreadLabel string   // the thread's human title, for the status-bar context indicator
	Engine      string   // engine name for PARTYLINE_ENGINE (claude/codex/gemini/antigravity)
	MCPs        []string // catalog MCP servers wired into this session (names; ctrl-\ m)

	// Group ties sessions that share one split window. Empty (the only value older workspace
	// files carry) means "its own window", so a file written before merging existed restores
	// exactly as it always did. The value is the Key of the group's first session.
	Group string `json:",omitempty"`
	// Layout is tmux's window_layout string, carried by the FIRST spec of a group so a resume
	// restores the pane sizes the human left, not an even split. Ignored when Group is empty.
	Layout string `json:",omitempty"`
}
