package main

import "strings"

import "testing"

// POSITIVES — every credential shape redactSecrets promises to catch. The assertion is two-sided:
// the secret must be gone AND the surrounding text must survive, because a redacted line that loses
// its context is no longer diagnostic.
func TestRedactSecretsPositives(t *testing.T) {
	cases := []struct {
		name, in, secret, keep string
	}{
		{"openai key", "error: OpenAI rejected sk-abcdefghijklmnop1234567890 (401)", "sk-abcdefghijklmnop1234567890", "OpenAI rejected"},
		{"anthropic key", "ANTHROPIC_API_KEY=sk-ant-api03-AAAAbbbbCCCCdddd1234", "sk-ant-api03", "ANTHROPIC_API_KEY="},
		{"github pat", "remote: fatal auth with ghp_16C7e42F292c6912E7710c838347Ae178B4a", "ghp_16C7e42F292c6912E7710c838347Ae178B4a", "remote: fatal auth with"},
		{"github oauth", "using gho_16C7e42F292c6912E7710c838347Ae178B4a now", "gho_16C7e42F292c6912E7710c838347Ae178B4a", "using"},
		{"github fine-grained", "token github_pat_11ABCDE0000AbCdEfGhIjK_xyz123 expired", "github_pat_11ABCDE0000AbCdEfGhIjK", "expired"},
		{"partyline token", "Authorization: Bearer plt_9f8e7d6c5b4a3210zzzz", "plt_9f8e7d6c5b4a3210zzzz", "Authorization:"},
		{"slack bot", "posting with xoxb-1234567890-0987654321-AbCdEf", "xoxb-1234567890", "posting with"},
		{"aws key id", "aws: the access key AKIAIOSFODNN7EXAMPLE is disabled", "AKIAIOSFODNN7EXAMPLE", "is disabled"},
		{"private key header", "cat id_rsa\n-----BEGIN OPENSSH PRIVATE KEY-----", "BEGIN OPENSSH PRIVATE KEY", "cat id_rsa"},
		{"labelled api key", `env: API_KEY=hunter2andmore`, "hunter2andmore", "API_KEY="},
		{"labelled password", "psql: password: correct-horse-battery", "correct-horse-battery", "password:"},
		{"labelled secret quoted", `config {"client_secret": "sh-abc-def-ghi"}`, "sh-abc-def-ghi", "client_secret"},
		{"bearer header", "curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig'", "eyJhbGciOiJIUzI1NiJ9", "Authorization: Bearer"},
		{"postgres url", "connect postgres://app:s3cr3tpw@db.internal:5432/main failed", "s3cr3tpw", "db.internal:5432/main"},
		{"postgresql url", "DSN postgresql://svc:pAssw0rdy@10.0.0.4/db", "pAssw0rdy", "postgresql://svc:"},
		{"high-entropy blob", "resp: aZ9bY8cX7dW6eV5fU4gT3hS2iR1jQ0kP9lO8 <- opaque", "aZ9bY8cX7dW6eV5fU4gT3hS2iR1jQ0kP9lO8", "opaque"},
	}
	for _, c := range cases {
		got := redactSecrets(c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("%s: secret %q survived: %s", c.name, c.secret, got)
		}
		if !strings.Contains(got, redactedMark) {
			t.Errorf("%s: no %s marker: %s", c.name, redactedMark, got)
		}
		if !strings.Contains(got, c.keep) {
			t.Errorf("%s: lost the surrounding context %q: %s", c.name, c.keep, got)
		}
	}
}

// NEGATIVES — the half that makes the tool worth having. A redactor that eats git SHAs, paths,
// branch names, or token COUNTS destroys the diagnostic value it exists to protect, so these lines
// must come back byte-identical.
func TestRedactSecretsNegativesUntouched(t *testing.T) {
	lines := []string{
		// A git SHA is 40 hex chars — mangling it would break every "which commit?" question.
		"HEAD is now at 9f2a4c1b8e7d6053a1b2c3d4e5f60718293a4b5c",
		"abbrev 9f2a4c1 Merge pull request #212 from darcyreno/feat-run-read",
		// md5 (32 hex) and sha256 (64 hex) checksums.
		"md5 d41d8cd98f00b204e9800998ecf8427e  dist.tar.gz",
		"sha256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		// Ordinary prose, including the bare words the label rule keys on.
		"the token expired and the secret was never set, so the password prompt appeared",
		"warning: no credentials found — falling back to the ambient session",
		// Token COUNTS: the exact signal the motivating bug ("completed with 0 tokens") depends on.
		"fresh_tokens: 4210 cache_read_tokens: 118904 cost_usd: 0.42",
		"max_tokens=200000 tokens: 0 duration_ms: 0",
		"Total tokens: 12 (fresh) +0 cached",
		// Paths and branch names: long, mixed case, digit-bearing — and load-bearing.
		"/Users/darcy/dev/partyline/internal/api/client.go:1947: EnqueueRun",
		"partyline--crank-4d0d9217-01-Migration-projects-max-repair-rounds",
		"switched to branch feat/MCP-ReadRun-2 tracking origin/feat/MCP-ReadRun-2",
		// A normal URL with no credentials in it.
		"GET https://partyline.sh/api/v1/runs/3f1a2b4c-5d6e-7f80-9012-3456789abcde 200",
		// Long lowercase identifiers (no uppercase → the entropy rule must not fire).
		"applyingmigrationtwentythreeforrunlogstablenow ok",
	}
	for _, l := range lines {
		if got := redactSecrets(l); got != l {
			t.Errorf("mangled a benign line:\n  in:  %s\n  out: %s", l, got)
		}
	}
}
