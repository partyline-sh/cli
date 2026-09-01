package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTasksRound2(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		literal bool
		want    []string
	}{
		{
			name: "comments, blanks, whitespace, and mid-line hash",
			lines: []string{
				"# comment",
				"",
				"  task one  ",
				"#",
				"deploy the # feature",
			},
			want: []string{"task one", "deploy the # feature"},
		},
		{
			// The chain-c842d926 worklist: every task is titled with a GitHub issue ref, so the whole
			// file read as comments and the run did nothing while reporting success.
			name: "issue-ref titles are all comments without --literal",
			lines: []string{
				"#570: the /loop-engineering capture page",
				"#569 Go: crank --max-repairs",
				"#569 web: project setting UI",
			},
			want: nil,
		},
		{
			name: "--literal takes every non-blank line verbatim",
			lines: []string{
				"#570: the /loop-engineering capture page",
				"",
				"  #569 Go: crank --max-repairs  ",
				"# comment-shaped, but still a task under --literal",
			},
			literal: true,
			want: []string{
				"#570: the /loop-engineering capture page",
				"#569 Go: crank --max-repairs",
				"# comment-shaped, but still a task under --literal",
			},
		},
	}

	// An empty worklist must FAIL the run, not finish it clean: the old code printed a note and
	// returned 0, so the daemon recorded `done` on a run that built nothing.
	t.Run("empty worklist exits non-zero", func(t *testing.T) {
		if emptyWorklistExit == 0 {
			t.Fatal("emptyWorklistExit must be non-zero so waitRun records `failed`")
		}
		for _, other := range []int{budgetPauseExit, verifyPauseExit, rateLimitExit, resumeAbortExit} {
			if emptyWorklistExit == other {
				t.Fatalf("emptyWorklistExit %d collides with an existing exit code", emptyWorklistExit)
			}
		}
		if msg := emptyWorklistError("b.txt", false).Error(); !strings.Contains(msg, "--literal") || !strings.Contains(msg, "#") {
			t.Fatalf("message must name the #-as-comment trap and the fix: %q", msg)
		}
		if msg := emptyWorklistError("b.txt", true).Error(); strings.Contains(msg, "--literal") {
			t.Fatalf("under --literal the file really is empty; do not suggest --literal: %q", msg)
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.txt")
			content := ""
			for _, l := range tt.lines {
				content += l + "\n"
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("writing temp file: %v", err)
			}

			got, err := parseTasks(path, tt.literal)
			if err != nil {
				t.Fatalf("parseTasks returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseTasks() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
