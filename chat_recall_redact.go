package main

import (
	"regexp"
	"strings"
)

var chatRecallPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
var chatRecallSecretShape = regexp.MustCompile(`\b(?:sk[_-][A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|AQ\.[A-Za-z0-9_.-]{16,}|AIza[A-Za-z0-9_-]{25,}|AKIA[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var chatRecallSecretLabel = regexp.MustCompile(`(?i)\b(?:password|passphrase|passwd|secret|token|api[ _-]?key|authorization|bearer|credential)s?\b\s*(?::|=|\bis\b|\bare\b)`)
var chatRecallLabelOnly = regexp.MustCompile(`(?i)^\s*(?:password|passphrase|passwd|secret|token|api[ _-]?key|authorization|credential)s?\s*:?[\s]*$`)

// This is best-effort output filtering, not a claim that arbitrary pasted
// credentials can be recognized. The original log stays intact on disk.
func redactChatRecall(text string) string {
	text = chatRecallPrivateKey.ReplaceAllString(text, "[private key redacted]")
	text = chatRecallSecretShape.ReplaceAllString(text, "[credential redacted]")
	lines := strings.Split(text, "\n")
	hideNext := false
	for i, line := range lines {
		if hideNext && strings.TrimSpace(line) != "" {
			lines[i] = "[credential redacted]"
			hideNext = false
			continue
		}
		if chatRecallSecretLabel.MatchString(line) || chatRecallLabelOnly.MatchString(line) {
			trimmed := strings.TrimSpace(line)
			hideNext = chatRecallLabelOnly.MatchString(line) || strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "=") || strings.HasSuffix(strings.ToLower(trimmed), " is")
			lines[i] = "[credential-bearing line redacted]"
		}
	}
	return strings.Join(lines, "\n")
}
