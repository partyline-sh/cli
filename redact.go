package main

import (
	"regexp"
	"strings"
)

// Secret redaction for anything we hand back to a model. Run logs are RAW build output: a failing
// command echoes its environment, a curl prints its Authorization header, a connection string shows
// up in a stack trace. Nothing here is authorization — the account token and RLS decide WHAT a
// caller may read — this is damage limitation on the way OUT, so a diagnosis never turns into
// credential exfiltration through the transcript.
//
// Design constraint, equally important: a redactor that mangles ordinary output destroys the tool.
// A git SHA, a file path, a branch name, and `fresh_tokens: 4210` must all survive verbatim, or an
// agent can no longer diagnose from what it reads. Every rule below is written to replace the SECRET
// and keep the surrounding text, and the entropy backstop is deliberately conservative (see below).

const redactedMark = "[redacted]"

// Prefixed credentials — provider-issued tokens whose shape is unambiguous, so these can be matched
// with no context and no false positives.
var redactPrefixed = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),              // OpenAI / Anthropic style (covers sk-ant-…)
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),         // GitHub classic PAT / OAuth / server / refresh
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{16,}`),       // GitHub fine-grained PAT
	regexp.MustCompile(`plt_[A-Za-z0-9_-]{12,}`),             // partyline account token
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),       // Slack
	regexp.MustCompile(`\bAKIA[0-9A-Z]{12,20}\b`),            // AWS access key id
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), // a key body pasted into output
}

// Labelled secrets — `API_KEY=…`, `password: …`, `client_secret: …`, `access_token=…`. The label is
// KEPT (it's the diagnostic part: which credential the process was reaching for) and only the value
// is dropped. A leading qualifier (`client_`, `access-`) is allowed so the common compound names
// match. Two guards keep run telemetry readable:
//   - the label list is SINGULAR (`token`, not `tokens`), and the TRAILING \b means `fresh_tokens:
//     4210` and `Total tokens: 12` cannot match at all — the `s` blocks the word boundary;
//   - a purely NUMERIC value is left alone (see redactSecrets) — a count is never a credential, and
//     "0 tokens" is the exact signal this tool exists to surface.
//
// The optional quote either side of the separator is what makes JSON output (`"client_secret": "…"`)
// match too; the numeric guard is what keeps `"token": 0` intact despite it.
var redactLabelled = regexp.MustCompile(`(?i)\b([A-Za-z_-]*(?:api[_-]?key|secret|token|password|passwd|credential))\b["']?(\s*[:=]\s*)["']?([^\s"',}]{3,})["']?`)

// `Bearer <value>` — the separator is whitespace, not `:`/`=`, so it needs its own rule.
var redactBearer = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)([A-Za-z0-9._~+/=-]{8,})`)

// Credentials embedded in a URL. Keeps the scheme, user, and host — which is exactly what you need
// to see ("it connected to the wrong database") — and drops only the password.
var redactURLCred = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@]+:)([^\s/@]+)(@)`)

// High-entropy backstop: one contiguous run of ≥32 base64/base62 characters that mixes upper, lower,
// AND digits. The mixed-case+digit requirement is what protects the diagnostic value:
//   - a git SHA (40 lowercase hex) has no uppercase → never matched
//   - an uppercase checksum has no lowercase → never matched
//   - prose and lowercase identifiers → never matched
//
// `/`, `-`, and `.` are excluded from the charset on purpose: including them would swallow long file
// paths (`Users/darcy/dev/…/frame001.png`) and branch names (`crank-4d0d9217-01-Migration-…`), which
// are load-bearing in a run diagnosis. The cost is that a secret containing `/` or `-` is only
// caught by the rules above — accepted, since those cover the provider formats we actually see.
var redactEntropy = regexp.MustCompile(`[A-Za-z0-9+_=]{32,}`)

// hexOnly reports whether s is pure hex — a digest (git SHA, md5, sha256), not a secret. Belt and
// braces alongside the mixed-case requirement, so a future charset change can't start eating SHAs.
func hexOnly(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// isNumeric reports whether s is just a number (optionally with a decimal point or separators) —
// i.e. a metric, not a secret.
func isNumeric(s string) bool {
	digits := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r == '.' || r == ',' || r == '_':
		default:
			return false
		}
	}
	return digits
}

func hasUpper(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0
}
func hasLower(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0
}
func hasDigit(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0
}

// redactSecrets rewrites one line, replacing credential-shaped substrings with [redacted] and
// leaving everything else byte-identical. Order matters: the unambiguous prefixed formats go first
// so `API_KEY=sk-…` reads the same as a bare `sk-…`, then the labelled/URL rules, then the entropy
// backstop last (it's the blunt one).
func redactSecrets(line string) string {
	for _, re := range redactPrefixed {
		line = re.ReplaceAllString(line, redactedMark)
	}
	line = redactLabelled.ReplaceAllStringFunc(line, func(m string) string {
		g := redactLabelled.FindStringSubmatch(m)
		if isNumeric(g[3]) {
			return m // a count / limit, not a credential — `fresh_tokens: 4210`
		}
		return g[1] + g[2] + redactedMark
	})
	line = redactBearer.ReplaceAllString(line, "${1}${2}"+redactedMark)
	line = redactURLCred.ReplaceAllString(line, "${1}"+redactedMark+"${3}")
	line = redactEntropy.ReplaceAllStringFunc(line, func(m string) string {
		if hexOnly(m) || !hasUpper(m) || !hasLower(m) || !hasDigit(m) {
			return m
		}
		return redactedMark
	})
	return line
}
