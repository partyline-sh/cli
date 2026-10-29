package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Scan source text (including comments), rather than a hand-maintained file list. The extended
// SGR pattern matches the 38;5 / 38;2 / 48;… prefix, so an escape whose colour values come from
// format verbs is caught; one whose 38 or 48 comes from a verb, or a hex colour assembled at run
// time, is not. Plain reset, emphasis and terminal controls do not match. Also reject basic ANSI
// colours.
func TestNoColourLiterals(t *testing.T) {
	colour := regexp.MustCompile(`(?i)#[0-9a-f]{6}\b|(?:38|48);(?:5|2);|\\(?:x1b|033|e)\[(?:[0-9]+;)*(?:3[0-7]|4[0-7]|9[0-7]|10[0-7])(?:;[0-9]+)*m`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == ".git" || path == "internal/theme" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if colour.MatchString(line) {
				t.Errorf("%s:%d: colour literal outside internal/theme", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Each table with a Docs column declares documentation coverage. Check every backtick-delimited
// key in its Key cell against either public page, case-sensitively and on token boundaries for
// word keys. Symbol keys match literally. Strip Markdown backticks and roff font/hyphen escapes;
// this accepts presentation changes without accepting e.g. ctrl-r as documentation for r.
func TestInventoryKeysDocumented(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	inventory := read("docs/plans/ui-key-inventory.md")
	public := read("web/src/app/(marketing)/docs/sessions/page.mdx") + "\n" + read("docs/partyline.1")
	public = strings.NewReplacer("`", "", `\fB`, "", `\fR`, "", `\fI`, "", `\-`, "-", `\e`, `\`, `\|`, "|").Replace(public)
	public = regexp.MustCompile(`([0-9])-([0-9])`).ReplaceAllString(public, "$1 $2")
	split := regexp.MustCompile(`(?:\\\||[^|])+`)
	keys := regexp.MustCompile("`([^`]+)`")
	docsCol, keyCol, checked := -1, -1, 0
	for i, line := range strings.Split(inventory, "\n") {
		if !strings.HasPrefix(line, "|") {
			docsCol, keyCol = -1, -1
			continue
		}
		cells := split.FindAllString(strings.Trim(line, "|"), -1)
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		if len(cells) > 0 && cells[0] == "Key" {
			for j, c := range cells {
				if c == "Docs" {
					docsCol = j
				}
				if c == "Key" {
					keyCol = j
				}
			}
			continue
		}
		for _, cell := range cells {
			if strings.Contains(strings.ToLower(cell), "to document") {
				t.Errorf("inventory:%d: unfinished documentation", i+1)
			}
		}
		if docsCol < 0 || len(cells) <= docsCol || strings.HasPrefix(cells[0], "---") {
			continue
		}
		if cells[docsCol] == "—" || cells[docsCol] == "removed" {
			continue
		}
		for _, match := range keys.FindAllStringSubmatch(cells[keyCol], -1) {
			key := strings.ReplaceAll(match[1], `\|`, "|")
			pattern := regexp.QuoteMeta(key)
			if regexp.MustCompile(`^[a-zA-Z0-9]`).MatchString(key) {
				pattern = `(?:^|[^a-zA-Z0-9_-])` + pattern
			}
			if regexp.MustCompile(`[a-zA-Z0-9]$`).MatchString(key) {
				pattern += `(?:$|[^a-zA-Z0-9_-])`
			}
			if !regexp.MustCompile(pattern).MatchString(public) {
				t.Errorf("inventory:%d: key %q missing from sessions page and man page", i+1, key)
			}
			checked++
		}
	}
	if checked < 100 {
		t.Fatalf("only checked %d keys; inventory tables were not parsed", checked)
	}
}

func TestContactSheet(t *testing.T) {
	got, err := contactSheet()
	if err != nil {
		t.Fatal(err)
	}
	const path = "docs/contact-sheet.txt"
	if *updateLoopsTab {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s changed; regenerate with go test -run '^TestContactSheet$' -update-loops-tab", path)
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatal("contact sheet contains ANSI escapes")
	}
	for _, heading := range []string{
		"ctrl-\\ menu / list", "ctrl-\\ menu / commands", "approval popup / list", "approval popup / brief",
		"approval popup / review", "approval popup / dispute", "approval popup / running run",
		"approval popup / confirm", "Loops board / list", "Loops board / detail", "Loops board / empty welcome",
		"chat popup / conversation", "popup form / choice", "popup form / pick", "popup form / long pick",
		"popup form / ask", "popup form / confirm", "popup form / note", "launcher popup / list",
		"launcher popup / preview", "launcher popup / search", "launcher popup / rename",
		"launcher popup / delete confirm", "launcher popup / permission picker", "launcher popup / help",
		"launcher popup / flash", "welcome / door 1", "empty state / no homes", "recover / no remote",
		"boot splash / working",
	} {
		if !strings.Contains(got, "=== "+heading+" ===\n") {
			t.Errorf("missing state: %s", heading)
		}
	}
	for _, frame := range strings.Split(got, "\n=== ")[1:] {
		_, body, ok := strings.Cut(frame, " ===\n")
		if !ok || strings.Count(strings.TrimSuffix(body, "\n"), "\n") != 24 {
			// Each frame has 24 screen rows and a blank separator (except the last frame).
			if strings.Count(body, "\n") != 24 {
				t.Fatal("contact sheet frame is not 24 rows")
			}
		}
	}
	again, err := contactSheet()
	if err != nil || again != got {
		t.Fatalf("contact sheet is not deterministic: %v", err)
	}
}
