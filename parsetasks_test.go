package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseTasksRound2(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []string
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
	}

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

			got, err := parseTasks(path)
			if err != nil {
				t.Fatalf("parseTasks returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseTasks() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
