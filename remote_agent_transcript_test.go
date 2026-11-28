package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectedTranscriptRequiresUniqueStartupOrExplicitNativeID(t *testing.T) {
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	agent := &detectedAgent{engine: "claude", process: agentProcess{started: start, argv: []string{"claude"}}}
	newSession := func(id string, at time.Time) aiSession {
		path := filepath.Join(t.TempDir(), id+".jsonl")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("{\"timestamp\":%q}\n", at.Format(time.RFC3339Nano))), 0600); err != nil {
			t.Fatal(err)
		}
		return aiSession{Tool: "claude", ID: id, Cwd: "/work/project", LastActive: start.Add(time.Minute), storePath: path}
	}
	fresh := newSession("11111111-1111-1111-1111-111111111111", start.Add(time.Second))
	old := newSession("22222222-2222-2222-2222-222222222222", start.Add(-time.Hour))
	delayed := newSession("44444444-4444-4444-4444-444444444444", start.Add(time.Minute))
	if detectedTranscriptSession(agent, "/work/project", []aiSession{delayed}) != nil {
		t.Fatal("delayed first prompt guessed without process-to-history evidence")
	}
	if s := detectedTranscriptSession(agent, "/work/project", []aiSession{old, fresh}); s == nil || s.ID != fresh.ID {
		t.Fatal("unique new history missing")
	}
	other := newSession("33333333-3333-3333-3333-333333333333", start)
	if detectedTranscriptSession(agent, "/work/project", []aiSession{fresh, other}) != nil {
		t.Fatal("ambiguous history exported")
	}
	if detectedTranscriptSession(agent, "/other/project", []aiSession{fresh}) != nil {
		t.Fatal("wrong directory exported")
	}
	if detectedTranscriptSession(agent, "/work/project", []aiSession{old}) != nil {
		t.Fatal("old history guessed from recent mtime")
	}
	agent.process.argv = []string{"claude", "--continue"}
	if detectedTranscriptSession(agent, "/work/project", []aiSession{fresh}) != nil {
		t.Fatal("latest continuation guessed")
	}
	agent.process.argv = []string{"claude", "--resume", old.ID}
	if s := detectedTranscriptSession(agent, "/work/project", []aiSession{old, fresh}); s == nil || s.ID != old.ID {
		t.Fatal("explicit native resume lost")
	}
	agent.process.argv = append(agent.process.argv, "--fork-session")
	if detectedTranscriptSession(agent, "/work/project", []aiSession{old, fresh}) != nil {
		t.Fatal("fork exported original history")
	}
	agent.process.argv = agent.process.argv[:len(agent.process.argv)-1]
	duplicate := old
	duplicate.storePath = fresh.storePath
	if detectedTranscriptSession(agent, "/work/project", []aiSession{old, duplicate}) != nil {
		t.Fatal("duplicated ID guessed")
	}
}
