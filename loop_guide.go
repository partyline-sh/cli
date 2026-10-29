package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/clispec"
)

const loopGuidePointer = "To run work through a partyline loop (planned, checked and reviewed), run `ptln loop " + clispec.LoopGuide + "`."

// Keep this separate from memory's seen timestamp: the pointer must reach sessions
// with no memory, including sessions with memory refresh disabled.
func loopGuidePointerForSession(sessionID string) string {
	key := "session:" + sessionID
	if sessionID == "" {
		key = chatSelfPaneKey()
		if key == "" {
			return ""
		}
	}
	path := filepath.Join(stateDir(), "loop-guide-seen", fmt.Sprintf("%x", sha256.Sum256([]byte(key))))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ""
	}
	_ = f.Close()
	return loopGuidePointer + "\n"
}

func renderLoopGuide(engine string) string {
	cmd := func(name string) string { return "ptln loop " + name }
	var b strings.Builder
	b.WriteString("Partyline loop workflow\n\nUse a single run for one unit of work you will build in this session. Use a batch for two or more ordered steps, each with its own brief.\n\n")
	fmt.Fprintf(&b, "Write a Markdown brief with these required sections: %s. Acceptance must contain one run: <command> (exit 0) or file: <path> (must exist) per line. Brief template:\n\n%s\n", strings.Join(loopBriefSections, ", "), loopBriefTemplate)
	fmt.Fprintf(&b, "Single run: `%s --brief <file> --checks \"<cmd>\"`\nBatch: `%s --brief <step1> --brief <step2> --checks \"<cmd>\"`\nRepeat --checks for multiple checks.\n\n", cmd(clispec.LoopStart), cmd(clispec.LoopBatch)+" "+clispec.LoopStart)
	b.WriteString("Partyline reviews the plan first. The person approves in the approval popup (ctrl-\\ then a); the session must never approve or operate that popup. Wait for approval and baseline checks to finish; build only when the loop tells you to start.\n\nFor a batch, contractors build each step, run checks and submit to review. The session only hears about failures; do not build the steps yourself. Partyline commits batch steps that pass and never pushes.\n\n")
	fmt.Fprintf(&b, "For a single run, build the approved brief and run the checks. Write a report with these required sections: %s. Then run `%s <id> --report <file>`. Report template:\n\n%s\n", strings.Join(loopReportSections, ", "), cmd(clispec.LoopReady), loopReportTemplate)
	if engine == "codex" || engine == "gemini" {
		fmt.Fprintf(&b, "End your turn. When woken (or given the next prompt), read results with `%s <id>`.\n", cmd(clispec.LoopInbox))
	} else {
		fmt.Fprintf(&b, "Run `%s <id>` as a background task and end your turn; its output delivers results. Restart the listener after each delivery.\n", cmd(clispec.LoopListen))
	}
	fmt.Fprintf(&b, "Check each finding against the code. Fix findings and submit another report, or dispute a finding with `%s <id> <finding> --reason \"<evidence>\"`. The configured dispute process decides it; the session must never approve its own dispute.\n", cmd(clispec.LoopDispute))
	return b.String()
}
