package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Only an identifiable, nonempty composer is eligible for a retry. Menus,
// working panes, shell prompts and collapsed/ambiguous drafts stay unclassified.
func remoteComposerParts(screen string) (string, string) {
	bottom := screenBottom(screen, 15)
	if _, why := screenIdleEmpty(bottom); why != "something is typed in its input box" {
		return "", ""
	}
	lines := strings.Split(bottom, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		plain := strings.Trim(stripColours(lines[i]), " │\t")
		for _, mark := range []string{"❯", "›"} {
			if !strings.HasPrefix(plain, mark) {
				continue
			}
			// Only a visible one-line draft with no other draft content is a match.
			// Recognize Codex's ruled footer; other layouts remain conservative.
			footer := false
			for _, line := range lines[i+1:] {
				p := strings.Trim(stripColours(line), " │\t")
				if p == "" {
					continue
				}
				if strings.Trim(p, "─━┄┈┌┐└┘╭╮╰╯") == "" {
					footer = true
					continue
				}
				if footer && (strings.HasPrefix(strings.ToLower(p), "gpt-") || strings.HasPrefix(p, "? for shortcuts")) {
					continue
				}
				return "", ""
			}
			return strings.TrimSpace(strings.TrimPrefix(plain, mark)), strings.Join(lines[i:], "\n")
		}
	}
	return "", ""
}
func remoteComposerFingerprint(screen string) string {
	line, raw := remoteComposerParts(screen)
	if line == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func remoteDeliveryState(screen, text, previous string) (string, string) {
	if screenActivity(stripColours(screenBottom(screen, 15))).State == "working" {
		return "agent_working", ""
	}
	line, _ := remoteComposerParts(screen)
	fingerprint := remoteComposerFingerprint(screen)
	// A one-line exact match is deliberately conservative. Wrapped, multiline or
	// collapsed pastes cannot prove which draft a later Enter would submit.
	if fingerprint != "" && (previous != "" && fingerprint == previous || !strings.Contains(text, "\n") && strings.TrimSpace(text) != "" && line == strings.TrimSpace(text)) {
		return "in_composer", fingerprint
	}
	return "delivered", ""
}
