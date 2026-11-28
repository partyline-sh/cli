package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/clispec"
)

func TestLoopGuide(t *testing.T) {
	spec, ok := clispec.Lookup("loop")
	if !ok {
		t.Fatal("loop missing from registry")
	}
	var help bytes.Buffer
	clispec.PrintHelp(&help, spec)
	if !strings.Contains(help.String(), "ptln loop guide") {
		t.Fatal("guide missing from help")
	}
	for _, path := range []string{"docs/partyline.1", "web/src/app/(marketing)/docs/sessions/page.mdx"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"ptln loop guide", "use a partyline loop for this"} {
			if !strings.Contains(string(data), want) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
	}
	for _, engine := range []string{"claude", "codex", "gemini"} {
		t.Run(engine, func(t *testing.T) {
			guide := renderLoopGuide(engine)
			want, absent := "ptln loop inbox <id>", "ptln loop listen <id>"
			if engine == "claude" {
				want, absent = absent, want
			}
			if !strings.Contains(guide, want) || strings.Contains(guide, absent) {
				t.Fatal(guide)
			}
			for _, sections := range [][]string{loopBriefSections, loopReportSections} {
				if !strings.Contains(guide, strings.Join(sections, ", ")) {
					t.Fatal("required sections differ")
				}
			}
			for _, template := range []string{loopBriefTemplate, loopReportTemplate} {
				if !strings.Contains(guide, template) {
					t.Fatal("missing template")
				}
			}
			for _, command := range regexp.MustCompile("ptln loop [a-z]+(?: start)?").FindAllString(guide, -1) {
				found := false
				for _, usage := range spec.Usage {
					found = found || strings.HasPrefix(usage, command+" ")
				}
				if !found {
					t.Fatalf("unregistered command %q", command)
				}
			}
			t.Setenv("HOME", t.TempDir())
			t.Setenv("PARTYLINE_MEMORY_REFRESH", "off")
			t.Setenv("PARTYLINE_SESSION_KEY", "")
			oldEngine, oldServer, oldPane := memoryHookEngine, tmuxServerUp, chatAncestorPane
			t.Cleanup(func() { memoryHookEngine, tmuxServerUp, chatAncestorPane = oldEngine, oldServer, oldPane })
			memoryHookEngine = func() (int, string) { return 1, engine }
			tmuxServerUp = func() bool { return false }
			pane := "%guide-test"
			chatAncestorPane = func() string { return pane }
			hook := func(id string) string {
				t.Helper()
				in, err := os.CreateTemp(t.TempDir(), "in")
				if err != nil {
					t.Fatal(err)
				}
				defer in.Close()
				_ = json.NewEncoder(in).Encode(map[string]string{"session_id": id})
				_, _ = in.Seek(0, 0)
				out, err := os.CreateTemp(t.TempDir(), "out")
				if err != nil {
					t.Fatal(err)
				}
				defer out.Close()
				oldIn, oldOut := os.Stdin, os.Stdout
				func() {
					defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
					os.Stdin, os.Stdout = in, out
					memoryHookMain()
				}()
				_, _ = out.Seek(0, 0)
				data, _ := io.ReadAll(out)
				if len(data) == 0 || engine == "claude" {
					return string(data)
				}
				var payload struct {
					Hook struct {
						Context string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(data, &payload); err != nil {
					t.Fatal(err)
				}
				return payload.Hook.Context
			}
			for _, id := range []string{"first", "second", ""} {
				if got := hook(id); got != loopGuidePointer+"\n" {
					t.Fatalf("first hook: %q", got)
				}
				if got := hook(id); got != "" {
					t.Fatalf("repeated hook: %q", got)
				}
			}
			pane = "%another-pane"
			if got := hook(""); got != loopGuidePointer+"\n" {
				t.Fatalf("new pane: %q", got)
			}
		})
	}
	block := renderMemoryMap("repo", []fact{mapFact("constraint", "Keep this fact.")})
	if strings.Count(block, loopGuidePointer) != 1 {
		t.Fatal(block)
	}
	for _, name := range memoryMapFiles {
		path := filepath.Join(t.TempDir(), name)
		before, after := "User prose before.\n\n", "\n\nUser prose after.\n"
		if err := os.WriteFile(path, []byte(before+mapBegin+"\nold\n"+mapEnd+after), 0o600); err != nil {
			t.Fatal(err)
		}
		injectManagedBlock(path, mapBegin, mapEnd, block)
		injectManagedBlock(path, mapBegin, mapEnd, block)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != before+block+after {
			t.Fatalf("changed outside markers: %s", data)
		}
	}
}
