// `ptln skill` — the org-level skill library (v1). A skill is the Agent Skills open standard:
// a <name>/SKILL.md with YAML frontmatter (name, description) + a markdown body. Skills are
// org-scoped and versioned server-side; enabled ones are injected into every agent run's workspace
// by the daemon so any engine can use them (claude reads .claude/skills, everything else .agents/skills).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/skillzip"
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
		skillList(rest)
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
	fmt.Print(`ptln skill — org skill library (Agent Skills: a <name>/ dir with SKILL.md)

  ptln skill push <dir> [--name <n>] [--description <d>]
        package the WHOLE skill dir (SKILL.md + scripts/references/assets/…) as a
        bundle and create it (or a new version) in your org. Reads <dir>/SKILL.md
        for name+description (--name/--description override). Skips .git/,
        node_modules/, .DS_Store; caps at 5MB/file, 20MB and 200 files total.
  ptln skill list [--installed]
        list your ORG's skills: name · version · enabled · description.
        --installed instead scans THIS machine (~/.claude/skills, ~/.agents/skills,
        and this project's ./.claude|.agents/skills) — push one with ` + "`" + `skill push <path>` + "`" + `.
  ptln skill pull <name> [--dir <dir>]
        unpack the full bundle into ./<name>/ (or <dir>/<name>/). Body-only skills
        fall back to writing just ./<name>/SKILL.md.
  ptln skill install [<name>]
        install a skill (or, with no name, ALL enabled org skills) into
        ~/.agents/skills/<name>/ and symlink ~/.claude/skills/<name>, so your
        interactive claude/codex/gemini sessions get them too. Unpacks the full
        bundle (scripts/assets included). Idempotent.
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
	// Always push the packaged bundle (the WHOLE dir), even when it's a lone SKILL.md — one code
	// path, and the server treats a one-file bundle the same as the legacy body-only push. The name +
	// description we validated locally ride along as multipart fields and override the frontmatter the
	// server re-parses from the zip.
	zipBytes, err := skillzip.Bundle(dir)
	if err != nil {
		fatal(err)
	}
	c := mustClient()
	storedName, version, err := c.PushSkillBundle(name, desc, zipBytes)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ pushed skill %q — version %d (%d KB bundle)\n", storedName, version, (len(zipBytes)+1023)/1024)
}

func skillList(args []string) {
	for _, a := range args {
		if a == "--installed" || a == "--local" {
			skillListInstalled()
			return
		}
	}
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

// installedSkill is a skill dir found on THIS machine (has a SKILL.md), with whether it carries any
// script so the list can flag it before the user pushes it into the shared org library.
type installedSkill struct {
	Name       string
	Path       string
	HasScripts bool
}

// discoverInstalledSkills scans the standard skill locations for installed skills — the machine's
// user roots (~/.claude/skills, ~/.agents/skills) and, when cwd is inside a project, that project's
// ./.claude/skills and ./.agents/skills. A subdirectory holding a SKILL.md is a skill. Deduped by
// name (first location wins) and sorted, so the same skill symlinked into both roots shows once.
func discoverInstalledSkills() []installedSkill {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".agents", "skills"))
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, filepath.Join(cwd, ".claude", "skills"), filepath.Join(cwd, ".agents", "skills"))
	}
	seen := map[string]bool{}
	var out []installedSkill
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // root absent — nothing to scan
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
				continue // not a skill (no SKILL.md)
			}
			name := e.Name()
			if seen[name] {
				continue // first location wins (a symlinked-in skill shows once)
			}
			seen[name] = true
			out = append(out, installedSkill{Name: name, Path: dir, HasScripts: dirHasScripts(dir)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// dirHasScripts reports whether a skill dir carries anything that would import as a script — any
// executable file, anything under a scripts/ dir, or a known script extension. Advisory only (drives
// the "scripts" flag in the listing); the server re-derives has_scripts authoritatively on push.
func dirHasScripts(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if strings.HasPrefix(rel, "scripts"+string(filepath.Separator)) || scriptExt(p) {
			found = true
			return nil
		}
		if info, e := d.Info(); e == nil && info.Mode()&0o111 != 0 {
			found = true
		}
		return nil
	})
	return found
}

// scriptExt reports whether a path has a known executable-script extension.
func scriptExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".sh", ".bash", ".zsh", ".py", ".js", ".ts", ".rb", ".pl", ".ps1":
		return true
	}
	return false
}

// skillListInstalled scans the local machine for installed skills — the "I already have skills, add
// them to the org" path. Reports name · scripts? · path so the user can `ptln skill push <path>`.
func skillListInstalled() {
	found := discoverInstalledSkills()
	if len(found) == 0 {
		fmt.Println("no installed skills found (looked in ~/.claude/skills, ~/.agents/skills, and this project)")
		return
	}
	for _, s := range found {
		scripts := "         "
		if s.HasScripts {
			scripts = "scripts  "
		}
		fmt.Printf("%-24s %s %s\n", s.Name, scripts, s.Path)
	}
	fmt.Printf("\n%d installed skill(s) — add one to your org with `ptln skill push <path>`\n", len(found))
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
	base := dir
	if base == "" {
		base = "."
	}
	out := filepath.Join(base, name)
	// Try the full bundle first; fall back to writing just SKILL.md for a body-only skill (404).
	if zipBytes, err := c.GetSkillBundle(name, 0); err == nil {
		if err := skillzip.Unzip(zipBytes, out); err != nil {
			fatal(fmt.Errorf("unpack bundle: %w", err))
		}
		fmt.Printf("✓ unpacked %s/ (bundle)\n", out)
		return
	} else if !errors.Is(err, api.ErrSkillNoBundle) {
		fatal(err)
	}
	sk, err := c.GetSkill(name)
	if err != nil {
		fatal(err)
	}
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
	var names []string
	if name != "" {
		if !gitwt.ValidSkillName(name) {
			fatal(fmt.Errorf("invalid skill name %q", name))
		}
		names = []string{name}
	} else {
		// Bare `install` = all ENABLED org skills.
		metas, err := c.ListSkills()
		if err != nil {
			fatal(err)
		}
		for _, m := range metas {
			if m.Enabled {
				names = append(names, m.Name)
			}
		}
		if len(names) == 0 {
			fmt.Println("no enabled skills to install")
			return
		}
	}
	n := installSkillsHome(c, names)
	fmt.Printf("✓ installed %d skill(s) to ~/.agents/skills (linked into ~/.claude/skills)\n", n)
}

// installSkillsHome installs each named skill into the USER's ~/.agents/skills/<name>/ and links
// ~/.claude/skills/<name> at each, so interactive sessions (claude reads .claude/skills; other engines
// read .agents/skills) pick them up. It unpacks the FULL bundle (scripts/assets included) when the skill
// has one, falling back to a body-only SKILL.md for legacy/prose skills. The target dir is wiped first
// so a shrunk bundle leaves no stale files. Idempotent; a bad name or fetch error is warned and skipped.
// Returns how many installed.
func installSkillsHome(c *api.Client, names []string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	agentsRoot := filepath.Join(home, ".agents", "skills")
	claudeRoot := filepath.Join(home, ".claude", "skills")
	installed := 0
	for _, name := range names {
		if !gitwt.ValidSkillName(name) {
			fmt.Fprintf(os.Stderr, "  (skill %q: invalid name — skipped)\n", name)
			continue
		}
		agentDir := filepath.Join(agentsRoot, name)
		_ = os.RemoveAll(agentDir) // fresh install: no stale files from a prior larger bundle
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", name, err)
			continue
		}
		// Full bundle first (scripts/assets); body-only SKILL.md fallback when the skill has no bundle.
		if zipBytes, err := c.GetSkillBundle(name, 0); err == nil {
			if err := skillzip.Unzip(zipBytes, agentDir); err != nil {
				fmt.Fprintf(os.Stderr, "  (skill %q: unpack failed: %v — skipped)\n", name, err)
				continue
			}
		} else if errors.Is(err, api.ErrSkillNoBundle) {
			sk, err := c.GetSkill(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  (skill %q: fetch failed, skipped: %v)\n", name, err)
				continue
			}
			if err := os.WriteFile(filepath.Join(agentDir, "SKILL.md"), []byte(sk.Body), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", name, err)
				continue
			}
		} else {
			fmt.Fprintf(os.Stderr, "  (skill %q: %v — skipped)\n", name, err)
			continue
		}
		if err := os.MkdirAll(claudeRoot, 0o755); err == nil {
			link := filepath.Join(claudeRoot, name)
			_ = os.RemoveAll(link)
			if err := os.Symlink(agentDir, link); err != nil {
				// Symlink-unavailable fallback (rare; non-unix): copy the whole skill dir.
				_ = copyTree(agentDir, link)
			}
		}
		installed++
	}
	return installed
}

// copyTree recursively copies src into dst (files + subdirs), preserving the exec bit. Best-effort
// fallback for the symlink-unavailable case; errors are surfaced to the caller, which ignores them.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if info, e := d.Info(); e == nil && info.Mode()&0o111 != 0 {
			perm = 0o755
		}
		return os.WriteFile(target, b, perm)
	})
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

// loadGitwtSkills converts the api skills (what the daemon staged) into the shape the worktree
// materializer needs, at the gitwt→api boundary so gitwt stays client-free. For a PACKAGE skill it
// reads the daemon-staged <skillsDir>/<name>.zip so the whole tree (scripts/assets) materializes;
// body-only skills carry just their SKILL.md body. The zip filename is the skill NAME — a path-
// injection vector — so it's re-validated with the slug rule before it becomes a path; an invalid
// name reads no bundle and degrades to body-only. A missing/unreadable zip is a clean body-only
// fallback (the daemon may have failed to stage it), never an error.
func loadGitwtSkills(skills []api.Skill, skillsDir string) []gitwt.Skill {
	out := make([]gitwt.Skill, 0, len(skills))
	for _, s := range skills {
		gs := gitwt.Skill{Name: s.Name, Body: s.Body}
		if skillsDir != "" && gitwt.ValidSkillName(s.Name) {
			if zip, err := os.ReadFile(filepath.Join(skillsDir, s.Name+".zip")); err == nil {
				gs.Bundle = zip
			}
		}
		out = append(out, gs)
	}
	return out
}
