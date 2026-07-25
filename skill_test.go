package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

func TestParseSkillMD(t *testing.T) {
	t.Run("standard frontmatter", func(t *testing.T) {
		fm, body := parseSkillMD("---\nname: pdf\ndescription: work with PDFs\n---\n# PDF\n\ndo stuff\n")
		if fm.Name != "pdf" || fm.Description != "work with PDFs" {
			t.Fatalf("frontmatter wrong: %+v", fm)
		}
		if strings.TrimSpace(body) != "# PDF\n\ndo stuff" {
			t.Fatalf("body wrong: %q", body)
		}
	})

	t.Run("no frontmatter → whole file is body", func(t *testing.T) {
		fm, body := parseSkillMD("# just a doc\n\nno header here\n")
		if fm.Name != "" || fm.Description != "" {
			t.Fatalf("expected empty frontmatter, got %+v", fm)
		}
		if !strings.Contains(body, "just a doc") {
			t.Fatalf("body should be the whole file: %q", body)
		}
	})

	t.Run("frontmatter only, no body", func(t *testing.T) {
		fm, body := parseSkillMD("---\nname: bare\ndescription: d\n---\n")
		if fm.Name != "bare" {
			t.Fatalf("name wrong: %+v", fm)
		}
		if strings.TrimSpace(body) != "" {
			t.Fatalf("expected empty body, got %q", body)
		}
	})
}

func TestSkillManifest(t *testing.T) {
	t.Run("empty for no skills", func(t *testing.T) {
		if m := skillManifest(nil); m != "" {
			t.Fatalf("expected empty manifest, got %q", m)
		}
	})

	t.Run("names + descriptions, invalid names dropped", func(t *testing.T) {
		m := skillManifest([]api.Skill{
			{Name: "pdf", Description: "work with PDFs"},
			{Name: "seo-audit", Description: "audit SEO"},
			{Name: "../evil", Description: "nope"}, // invalid slug → dropped
			{Name: "nodesc", Description: ""},      // name only
		})
		if !strings.HasPrefix(m, "Installed skills (use when relevant): ") {
			t.Fatalf("bad prefix: %q", m)
		}
		if !strings.Contains(m, "pdf — work with PDFs") {
			t.Errorf("missing pdf entry: %q", m)
		}
		if !strings.Contains(m, "seo-audit — audit SEO") {
			t.Errorf("missing seo-audit entry: %q", m)
		}
		if strings.Contains(m, "evil") {
			t.Errorf("invalid-name skill leaked into manifest: %q", m)
		}
		if !strings.Contains(m, "nodesc") {
			t.Errorf("name-only skill should still appear: %q", m)
		}
		if !strings.HasSuffix(m, ".") {
			t.Errorf("manifest should end with a period: %q", m)
		}
	})
}

// loadSkillSet reads the daemon-staged skills.json (the crank --skills-dir contract). Missing/garbage
// is a no-skills no-op, never an error.
func TestLoadSkillSet(t *testing.T) {
	dir := t.TempDir()
	if got := loadSkillSet(dir); got != nil {
		t.Errorf("missing skills.json should yield nil, got %v", got)
	}
	if got := loadSkillSet(""); got != nil {
		t.Errorf("empty dir should yield nil, got %v", got)
	}
	json := `[{"name":"pdf","description":"PDFs","body":"do it","version":3}]`
	if err := os.WriteFile(filepath.Join(dir, "skills.json"), []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadSkillSet(dir)
	if len(got) != 1 || got[0].Name != "pdf" || got[0].Body != "do it" || got[0].Version != 3 {
		t.Fatalf("round-trip wrong: %+v", got)
	}
	// garbage → no-op, never a panic/error
	_ = os.WriteFile(filepath.Join(dir, "skills.json"), []byte("not json"), 0o600)
	if got := loadSkillSet(dir); got != nil {
		t.Errorf("garbage skills.json should yield nil, got %v", got)
	}
}
