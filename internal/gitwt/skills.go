package gitwt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"partyline.sh/partyline/internal/skillzip"
)

// skillNameRe is the ONLY shape a skill name may take before it becomes a path. It matches the
// server-side + web slug rule exactly: a dir-safe lowercase slug, 1–39 chars, starting with an
// alphanumeric. This is the path-injection boundary — a name that reaches MaterializeSkills becomes
// <worktree>/.agents/skills/<name>/, so anything with "/", "..", "~", whitespace, an absolute prefix,
// or uppercase is rejected here and never touches the filesystem.
var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

// ValidSkillName reports whether name is a safe skill slug. Callers MUST re-validate every name that
// crosses a trust boundary (a server response, a SKILL.md frontmatter field) right before it becomes a
// path — never trust that it was checked upstream.
func ValidSkillName(name string) bool { return skillNameRe.MatchString(name) }

// Skill is the minimum a materializer needs: a validated slug + the SKILL.md body, plus an OPTIONAL
// packaged bundle (the whole skill dir as a zip). Deliberately NOT the api.Skill type — gitwt is a
// low-level utility and must not depend on the HTTP client. The caller converts (api.Skill →
// gitwt.Skill) at the boundary. When Bundle is non-empty it's the source of truth (scripts + assets +
// SKILL.md); Body is the fallback for a legacy/prose skill with no bundle.
type Skill struct {
	Name   string
	Body   string
	Bundle []byte // optional packaged zip (SKILL.md + scripts/references/assets); nil = body-only
}

// Per-engine skill-loading, smoke-tested 2026-07-14 with a canary skill (a SKILL.md whose body held
// a phrase found nowhere else) + the run manifest, headless, read-only:
//   - claude 2.1.209  — VERIFIED: reads .claude/skills/<name> (the symlink) and returned the canary.
//   - codex 0.140.0   — VERIFIED: reads .agents/skills natively under `codex exec --sandbox
//     read-only` and returned the canary.
//   - gemini 0.45.2   — UNVERIFIED on this machine: the CLI refused to run at all (IneligibleTierError
//     — Google deprecated the individual free tier). NOT a partyline issue; gemini
//     adopted the .agents/skills standard, so it should work with a supported
//     account. The manifest ships regardless, so name+description reach context even
//     if native file discovery is off.
//   - antigravity     — N/A headless (it refuses non-interactive tool use — see engine adapters);
//     skills reach interactive/party sessions via `ptln skill install` only.
//
// Takeaway: the two engines that actually run headless builds today (claude, codex) both load skills;
// the manifest line is the cross-engine floor when native on-demand discovery is unavailable.
//
// MaterializeSkills injects org skills into a fresh worktree so ANY engine can use them: it writes
// <worktree>/.agents/skills/<name>/SKILL.md (the open Agent-Skills location every non-Claude engine
// reads) and links <worktree>/.claude/skills/<name> at it (where Claude Code reads). Best-effort per
// skill, exactly like SeedInclude — a bad name is skipped+logged and never materialized, and an
// individual write failure skips that skill without failing the others. The injected dirs are added to
// the worktree's local git exclude so `git add -A` (commitWorktree) never stages them as the worker's
// changes.
func MaterializeSkills(worktree string, skills []Skill) error {
	if worktree == "" || len(skills) == 0 {
		return nil
	}
	wrote := false
	for _, s := range skills {
		// Re-validate at the path boundary — a server-returned name is untrusted input.
		if !ValidSkillName(s.Name) {
			fmt.Fprintf(os.Stderr, "  (skill %q: invalid name — skipped, not materialized)\n", s.Name)
			continue
		}
		agentDir := filepath.Join(worktree, ".agents", "skills", s.Name)
		skillFile := filepath.Join(agentDir, "SKILL.md")
		// A packaged skill unpacks its WHOLE tree (SKILL.md + scripts/assets) via skillzip — the trust
		// boundary that re-validates every entry (zip-slip / zip-bomb / traversal) before it lands. On any
		// unpack failure fall back to writing just the SKILL.md body, so the skill still injects minimally
		// rather than vanishing. A body-only skill takes the write path directly.
		materialized := false
		if len(s.Bundle) > 0 {
			if err := skillzip.Unzip(s.Bundle, agentDir); err != nil {
				// Unzip aborts mid-write on a cap violation (e.g. a lying-low zip bomb); clear the partial
				// so a rejected bundle can't leave megabytes on disk, then fall back to body-only.
				_ = os.RemoveAll(agentDir)
				fmt.Fprintf(os.Stderr, "  (skill %q: bundle unpack failed (%v) — falling back to SKILL.md only)\n", s.Name, err)
			} else if _, err := os.Stat(skillFile); err != nil {
				// A bundle with no SKILL.md at its root shouldn't happen (the server requires it), but if
				// it does, fall through to writing the body so the skill isn't left headless.
				fmt.Fprintf(os.Stderr, "  (skill %q: bundle had no SKILL.md — writing body)\n", s.Name)
			} else {
				materialized = true
			}
		}
		if !materialized {
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", s.Name, err)
				continue
			}
			if err := os.WriteFile(skillFile, []byte(s.Body), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", s.Name, err)
				continue
			}
		}
		wrote = true
		// Claude reads .claude/skills/<name>/SKILL.md; point it at the .agents copy with a relative
		// symlink so there's ONE source of truth. If the platform/filesystem rejects symlinks, fall
		// back to copying the file so Claude still sees the skill.
		claudeParent := filepath.Join(worktree, ".claude", "skills")
		if err := os.MkdirAll(claudeParent, 0o755); err != nil {
			continue // .agents copy already landed; other engines still get it
		}
		link := filepath.Join(claudeParent, s.Name)
		_ = os.RemoveAll(link) // idempotent across a resumed task's re-run
		rel := filepath.Join("..", "..", ".agents", "skills", s.Name)
		if err := os.Symlink(rel, link); err != nil {
			_ = os.MkdirAll(link, 0o755)
			_ = copyFile(skillFile, filepath.Join(link, "SKILL.md"))
		}
	}
	if wrote {
		excludeInWorktree(worktree, ".agents/skills/", ".claude/skills/")
	}
	return nil
}

// excludeInWorktree appends patterns to the worktree's LOCAL git exclude (.git/info/exclude, resolved
// per-worktree via rev-parse) so injected files are invisible to `git status`/`git add -A` without
// touching the repo's tracked .gitignore. Idempotent: a pattern already present is not re-added.
// Best-effort — a git/IO failure just means the injected dirs might show as untracked (never fatal).
func excludeInWorktree(worktree string, patterns ...string) {
	p, err := git(worktree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktree, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	have := map[string]bool{}
	existing, _ := os.ReadFile(p)
	for _, ln := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(ln)] = true
	}
	var add strings.Builder
	for _, pat := range patterns {
		if !have[pat] {
			add.WriteString(pat + "\n")
		}
	}
	if add.Len() == 0 {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(add.String())
}
