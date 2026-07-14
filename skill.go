// `ptln skill` — the org-level skill library (v1). A skill is the Agent Skills open standard:
// a <name>/SKILL.md with YAML frontmatter (name, description) + a markdown body. Skills are
// org-scoped and versioned server-side; enabled ones are injected into every agent run's workspace
// by the daemon so any engine can use them (claude reads .claude/skills, everything else .agents/skills).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

func skillMain(args []string) {
	if len(args) == 0 {
		skillUsage()
		return
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "push":
		skillPush(rest)
	case "list", "ls":
		skillList()
	case "pull":
		skillPull(rest)
	case "install":
		skillInstall(rest)
	case "help", "-h", "--help":
		skillUsage()
	default:
		fatal(fmt.Errorf("unknown: ptln skill %s (push|list|pull|install)", sub))
	}
}

func skillUsage() {
	fmt.Print(`ptln skill — org skill library (Agent Skills: <name>/SKILL.md)

  ptln skill push <dir> [--name <n>] [--description <d>]
        read <dir>/SKILL.md (YAML frontmatter name+description + markdown body),
        validate the name slug, and create it (or a new version) in your org.
  ptln skill list
        list your org's skills: name · version · enabled · description.
  ptln skill pull <name> [--dir <dir>]
        write the latest body to ./<name>/SKILL.md (or <dir>/<name>/SKILL.md).
  ptln skill install [<name>]
        install a skill (or, with no name, ALL enabled org skills) into
        ~/.agents/skills/<name>/SKILL.md and symlink ~/.claude/skills/<name>,
        so your interactive claude/codex/gemini sessions get them too. Idempotent.
`)
}

// skillFrontmatter is the SKILL.md YAML header (Agent Skills standard).
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseSkillMD splits a SKILL.md into its frontmatter (name, description) and the markdown body. The
// standard is a leading `---` fenced YAML block. A file with no frontmatter still parses (empty meta,
// whole file as body) so the caller's --name/--description overrides can supply the fields.
func parseSkillMD(content string) (skillFrontmatter, string) {
	s := strings.TrimPrefix(content, "\ufeff") // strip a UTF-8 BOM if present
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return skillFrontmatter{}, content
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	// Find the closing fence: a line that is exactly "---".
	end := -1
	for _, marker := range []string{"\n---\n", "\n---\r\n"} {
		if i := strings.Index(rest, marker); i >= 0 {
			end = i
			rest = rest[:i] + "\x00" + rest[i+len(marker):] // mark the split point
			break
		}
	}
	if end < 0 {
		// Also handle a frontmatter block that ends the file with a trailing "---".
		if strings.HasSuffix(strings.TrimRight(rest, "\r\n"), "\n---") {
			yamlPart := strings.TrimSuffix(strings.TrimRight(rest, "\r\n"), "\n---")
			var fm skillFrontmatter
			_ = yaml.Unmarshal([]byte(yamlPart), &fm)
			return fm, ""
		}
		return skillFrontmatter{}, content
	}
	parts := strings.SplitN(rest, "\x00", 2)
	var fm skillFrontmatter
	_ = yaml.Unmarshal([]byte(parts[0]), &fm)
	body := ""
	if len(parts) == 2 {
		body = parts[1]
	}
	return fm, body
}

func skillPush(args []string) {
	var dir, nameOv, descOv string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i++; i < len(args) {
				nameOv = strings.TrimSpace(args[i])
			}
		case "--description", "--desc":
			if i++; i < len(args) {
				descOv = strings.TrimSpace(args[i])
			}
		default:
			if !strings.HasPrefix(args[i], "-") && dir == "" {
				dir = args[i]
			}
		}
	}
	if dir == "" {
		fatal(fmt.Errorf("usage: ptln skill push <dir> [--name <n>] [--description <d>]"))
	}
	mdPath := filepath.Join(dir, "SKILL.md")
	b, err := os.ReadFile(mdPath)
	if err != nil {
		fatal(fmt.Errorf("no SKILL.md in %s — a skill is a directory containing SKILL.md (%w)", dir, err))
	}
	fm, body := parseSkillMD(string(b))
	name := fm.Name
	if nameOv != "" {
		name = nameOv
	}
	desc := fm.Description
	if descOv != "" {
		desc = descOv
	}
	name = strings.TrimSpace(name)
	if name == "" {
		fatal(fmt.Errorf("no skill name — set `name:` in %s frontmatter or pass --name", mdPath))
	}
	if !gitwt.ValidSkillName(name) {
		fatal(fmt.Errorf("invalid skill name %q — must be a slug: lowercase a-z/0-9/-, start alphanumeric, ≤39 chars", name))
	}
	if strings.TrimSpace(body) == "" {
		fatal(fmt.Errorf("SKILL.md has no body — the markdown after the frontmatter is the skill's instructions"))
	}
	c := mustClient()
	storedName, version, err := c.PushSkill(name, desc, body)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ pushed skill %q — version %d\n", storedName, version)
}

func skillList() {
	c := mustClient()
	skills, err := c.ListSkills()
	if err != nil {
		fatal(err)
	}
	if len(skills) == 0 {
		fmt.Println("no skills yet — create one with `ptln skill push <dir>`")
		return
	}
	for _, s := range skills {
		state := "disabled"
		if s.Enabled {
			state = "enabled"
		}
		fmt.Printf("%-24s v%-3d %-9s %s\n", s.Name, s.Version, state, s.Description)
	}
}

func skillPull(args []string) {
	var name, dir string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i++; i < len(args) {
				dir = args[i]
			}
		default:
			if !strings.HasPrefix(args[i], "-") && name == "" {
				name = args[i]
			}
		}
	}
	if name == "" {
		fatal(fmt.Errorf("usage: ptln skill pull <name> [--dir <dir>]"))
	}
	if !gitwt.ValidSkillName(name) {
		fatal(fmt.Errorf("invalid skill name %q", name))
	}
	c := mustClient()
	sk, err := c.GetSkill(name)
	if err != nil {
		fatal(err)
	}
	base := dir
	if base == "" {
		base = "."
	}
	out := filepath.Join(base, sk.Name)
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}
	dst := filepath.Join(out, "SKILL.md")
	if err := os.WriteFile(dst, []byte(sk.Body), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ wrote %s (version %d)\n", dst, sk.Version)
}

func skillInstall(args []string) {
	var name string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && name == "" {
			name = a
		}
	}
	c := mustClient()
	var skills []api.Skill
	if name != "" {
		if !gitwt.ValidSkillName(name) {
			fatal(fmt.Errorf("invalid skill name %q", name))
		}
		sk, err := c.GetSkill(name)
		if err != nil {
			fatal(err)
		}
		skills = []api.Skill{{Name: sk.Name, Description: sk.Description, Body: sk.Body, Version: sk.Version}}
	} else {
		// Bare `install` = all ENABLED org skills. Bodies aren't in the list, so fetch each.
		metas, err := c.ListSkills()
		if err != nil {
			fatal(err)
		}
		for _, m := range metas {
			if !m.Enabled {
				continue
			}
			sk, err := c.GetSkill(m.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  (skill %q: fetch failed, skipped: %v)\n", m.Name, err)
				continue
			}
			skills = append(skills, api.Skill{Name: sk.Name, Description: sk.Description, Body: sk.Body, Version: sk.Version})
		}
		if len(skills) == 0 {
			fmt.Println("no enabled skills to install")
			return
		}
	}
	n := installSkillsHome(skills)
	fmt.Printf("✓ installed %d skill(s) to ~/.agents/skills (linked into ~/.claude/skills)\n", n)
}

// installSkillsHome writes skills into the USER's ~/.agents/skills/<name>/SKILL.md and links
// ~/.claude/skills/<name> at each, so interactive sessions (claude reads .claude/skills; other engines
// read .agents/skills) pick them up. Idempotent; a bad name is skipped. Returns how many installed.
func installSkillsHome(skills []api.Skill) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	agentsRoot := filepath.Join(home, ".agents", "skills")
	claudeRoot := filepath.Join(home, ".claude", "skills")
	installed := 0
	for _, s := range skills {
		if !gitwt.ValidSkillName(s.Name) {
			fmt.Fprintf(os.Stderr, "  (skill %q: invalid name — skipped)\n", s.Name)
			continue
		}
		agentDir := filepath.Join(agentsRoot, s.Name)
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", s.Name, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(agentDir, "SKILL.md"), []byte(s.Body), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", s.Name, err)
			continue
		}
		if err := os.MkdirAll(claudeRoot, 0o755); err == nil {
			link := filepath.Join(claudeRoot, s.Name)
			_ = os.RemoveAll(link)
			if err := os.Symlink(agentDir, link); err != nil {
				_ = os.MkdirAll(link, 0o755)
				_ = copyFileTo(filepath.Join(agentDir, "SKILL.md"), filepath.Join(link, "SKILL.md"))
			}
		}
		installed++
	}
	return installed
}

// copyFileTo is a tiny best-effort file copy for the symlink-unavailable fallback.
func copyFileTo(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// skillManifest builds the short "Installed skills" line prepended to a worker/decompose prompt so the
// engine KNOWS which skills exist and when to reach for them (implicit skill-matching quality varies by
// engine, so we make it explicit). Returns "" for no skills, so callers can concatenate unconditionally.
func skillManifest(skills []api.Skill) string {
	var parts []string
	for _, s := range skills {
		if !gitwt.ValidSkillName(s.Name) {
			continue
		}
		d := strings.TrimSpace(s.Description)
		if d == "" {
			parts = append(parts, s.Name)
		} else {
			parts = append(parts, s.Name+" — "+d)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Installed skills (use when relevant): " + strings.Join(parts, "; ") + "."
}

// loadSkillSet reads a run's staged skill set (written by writeRunSkills) from <dir>/skills.json. Best-
// effort: a missing/malformed file yields no skills, never an error — a run degrades to no skills, it
// never fails on skill loading (same posture as globals).
func loadSkillSet(dir string) []api.Skill {
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "skills.json"))
	if err != nil {
		return nil
	}
	var skills []api.Skill
	if err := json.Unmarshal(b, &skills); err != nil {
		return nil
	}
	return skills
}

// toGitwtSkills converts the api skills (what the daemon fetches) into the minimal shape the worktree
// materializer needs (name + body), at the gitwt→api boundary so gitwt stays client-free.
func toGitwtSkills(skills []api.Skill) []gitwt.Skill {
	out := make([]gitwt.Skill, 0, len(skills))
	for _, s := range skills {
		out = append(out, gitwt.Skill{Name: s.Name, Body: s.Body})
	}
	return out
}
