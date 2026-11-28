package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Missing or ambiguous native history stays unsupported. Terminal text is never
// promoted into a conversation, and inferred IDs never become persistent tags.
func detectedTranscript(pane, target string) (any, error) {
	result := struct {
		Target  string            `json:"target"`
		Entries []transcriptEntry `json:"entries"`
	}{Target: target}
	b, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_pid}\t#{pane_tty}").Output()
	f := strings.Fields(string(b))
	if err != nil || len(f) != 2 {
		return nil, fmt.Errorf("pane unavailable")
	}
	agent, err := detectPaneAgent(f[0], f[1])
	if err != nil || agent == nil || agent.target() != target {
		return nil, fmt.Errorf("agent changed")
	}
	if agent.engine != "claude" && agent.engine != "codex" {
		return result, nil
	}
	cwd, err := detectedProcessCwd(agent.process.pid)
	if err != nil {
		return result, nil
	}
	session := locateDetectedTranscript(agent, cwd)
	if session != nil {
		result.Entries, err = cachedTranscript(session.storePath, session.Tool)
		if err != nil {
			return nil, err
		}
	}
	current, err := detectPaneAgent(f[0], f[1])
	if err != nil || current == nil || current.target() != target {
		return nil, fmt.Errorf("agent changed")
	}
	return result, nil
}

func detectedProcessCwd(pid int) (string, error) {
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
		return cwd, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	b, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "n/") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}
	return "", fmt.Errorf("process directory unavailable")
}

func locateDetectedTranscript(agent *detectedAgent, cwd string) *aiSession {
	// Cache only discovery, never the live process check. Refresh positive matches
	// too: a second candidate must invalidate a formerly unique association.
	key := agent.target() + "\x00" + cwd
	transcriptLocations.Lock()
	defer transcriptLocations.Unlock()
	if entry, ok := transcriptLocations.entries[key]; ok && time.Since(entry.checked) < 3*time.Second {
		return entry.session
	}
	var candidates []aiSession
	for _, root := range loadSessionRoots() {
		if agent.engine == "claude" {
			candidates = append(candidates, claudeSessions(root.Home)...)
		} else {
			candidates = append(candidates, codexSessions(root.Home)...)
		}
	}
	match := detectedTranscriptSession(agent, cwd, candidates)
	if len(transcriptLocations.entries) >= 8 {
		var oldest string
		for k, entry := range transcriptLocations.entries {
			if oldest == "" || entry.checked.Before(transcriptLocations.entries[oldest].checked) {
				oldest = k
			}
		}
		delete(transcriptLocations.entries, oldest)
	}
	transcriptLocations.entries[key] = transcriptLocation{session: match, checked: time.Now()}
	return match
}

func detectedTranscriptSession(agent *detectedAgent, cwd string, candidates []aiSession) *aiSession {
	if cwd == "" || agent.process.started.IsZero() {
		return nil
	}
	explicit := detectedSessionID(agent)
	for _, arg := range agent.process.argv {
		if arg == "--fork-session" {
			return nil
		}
	}
	if explicit == "" {
		for _, arg := range agent.process.argv[1:] {
			// Continuing the latest history is not evidence of its native ID.
			if arg == "resume" || arg == "--resume" || arg == "-r" || arg == "--continue" || arg == "-c" {
				return nil
			}
		}
	}
	var match *aiSession
	for i := range candidates {
		s := &candidates[i]
		if s.Tool != agent.engine || filepath.Clean(s.Cwd) != filepath.Clean(cwd) || s.LastActive.Before(agent.process.started) {
			continue
		}
		if explicit != "" {
			if s.ID != explicit {
				continue
			}
		} else {
			started := transcriptFirstTimestamp(s.storePath)
			// ps resolves to seconds. Only a new native history created at process
			// startup qualifies; resumed/late/unknown histories need an explicit ID.
			if started.IsZero() || started.Before(agent.process.started.Add(-time.Second)) || started.After(agent.process.started.Add(5*time.Second)) {
				continue
			}
		}
		if match != nil && match.storePath != s.storePath {
			return nil
		}
		copy := *s
		match = &copy
	}
	return match
}

func detectedSessionID(agent *detectedAgent) string {
	args := agent.process.argv
	for i, arg := range args {
		if (agent.engine == "claude" && (arg == "--resume" || arg == "-r" || arg == "--session-id") || agent.engine == "codex" && arg == "resume") && i+1 < len(args) && nativeSessionID(args[i+1]) {
			return args[i+1]
		}
		for _, flag := range []string{"--resume=", "--session-id="} {
			if agent.engine == "claude" && strings.HasPrefix(arg, flag) && nativeSessionID(strings.TrimPrefix(arg, flag)) {
				return strings.TrimPrefix(arg, flag)
			}
		}
	}
	return ""
}

func nativeSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func transcriptFirstTimestamp(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, 256*1024))
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for n := 0; n < 32 && scanner.Scan(); n++ {
		var event struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Timestamp != "" {
			ts, _ := time.Parse(time.RFC3339Nano, event.Timestamp)
			return ts
		}
	}
	return time.Time{}
}
