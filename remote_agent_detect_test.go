package main

import (
	"testing"
	"time"
)

func TestForegroundAgentTracksShellChildrenAndExit(t *testing.T) {
	start := time.Unix(1800000000, 0)
	proc := func(pid, parent, group, foreground int, argv ...string) agentProcess {
		return agentProcess{pid, parent, group, foreground, start, argv}
	}
	for _, tt := range []struct {
		name string
		rows []agentProcess
		want string
		pid  int
	}{
		{"zsh to claude", []agentProcess{proc(1, 0, 1, 2, "zsh"), proc(2, 1, 2, 2, "claude.exe")}, "claude", 2},
		{"node wrapper to codex", []agentProcess{proc(1, 0, 1, 2, "zsh"), proc(2, 1, 2, 2, "node", "/opt/bin/codex"), proc(3, 2, 2, 2, "/vendor/codex")}, "codex", 3},
		{"agent owns foreground tool", []agentProcess{proc(1, 0, 1, 3, "zsh"), proc(2, 1, 2, 3, "claude"), proc(3, 2, 3, 3, "git", "status")}, "claude", 2},
		{"background agent ignored", []agentProcess{proc(1, 0, 1, 1, "zsh"), proc(2, 1, 2, 1, "claude")}, "", 0},
		{"agent exited", []agentProcess{proc(1, 0, 1, 1, "zsh")}, "", 0},
		{"node app ignored", []agentProcess{proc(1, 0, 1, 2, "zsh"), proc(2, 1, 2, 2, "node", "/srv/app.js", "codex")}, "", 0},
		{"unrelated tty process ignored", []agentProcess{proc(1, 0, 1, 2, "zsh"), proc(2, 90, 2, 2, "claude")}, "", 0},
		{"ambiguous foreground agents", []agentProcess{proc(1, 0, 1, 2, "zsh"), proc(2, 1, 2, 2, "claude"), proc(3, 1, 2, 2, "codex")}, "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := foregroundAgent(1, tt.rows)
			if tt.want == "" {
				if a != nil {
					t.Fatal(a.engine)
				}
				return
			}
			if a == nil || a.engine != tt.want || a.process.pid != tt.pid {
				t.Fatalf("agent=%+v want %s pid %d", a, tt.want, tt.pid)
			}
		})
	}
}

func TestParseAgentProcessesPinsStartTime(t *testing.T) {
	rows := parseAgentProcesses("1 0 1 2 Fri Oct 23 17:30:07 2026 -zsh\n2 1 2 2 Fri Oct 23 17:30:49 2026 node /usr/lib/node_modules/@openai/codex/bin/codex.js\nbad row")
	if len(rows) != 2 || rows[1].pid != 2 || rows[1].started.Location() != time.UTC || rows[1].argv[0] != "node" {
		t.Fatal(rows)
	}
}
