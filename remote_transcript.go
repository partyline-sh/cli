package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const transcriptTextLimit = 8192
const transcriptSnapshotLimit = 48 * 1024

type transcriptTool struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Status string `json:"status"`
	id     string
}
type transcriptEntry struct {
	Role  string           `json:"role"`
	Text  string           `json:"text"`
	Tools []transcriptTool `json:"tools"`
}

// An unreasonable ID cannot be safely correlated. Do not truncate into collisions.
func transcriptToolID(id string) string {
	if len(id) > 512 {
		return ""
	}
	return id
}

func transcriptCut(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "\n[cut]"
}

// Only exact native IDs are evidence of identity. In particular a new-* launcher
// key is not a conversation ID; guessing from mtime risks exporting another chat.
func transcriptSession(key, engine string, sessions []aiSession) *aiSession {
	if key == "" || strings.HasPrefix(key, "new-") || (engine != "claude" && engine != "codex") {
		return nil
	}
	var match *aiSession
	for i := range sessions {
		s := &sessions[i]
		if s.ID == key && s.Tool == engine {
			if match != nil && match.storePath != s.storePath {
				return nil
			}
			match = s
		}
	}
	if match == nil {
		return nil
	}
	copy := *match
	return &copy
}

func remoteTranscript(topic string) (any, error) {
	inventory, err := remoteSnapshot("sessions", "focus")
	if err != nil {
		return nil, err
	}
	pane := strings.TrimPrefix(topic, "transcript:")
	for _, row := range inventory.([]map[string]string) {
		if row["pane"] != pane {
			continue
		}
		if row["key"] == "" || strings.HasPrefix(row["key"], "new-") || (row["engine"] != "claude" && row["engine"] != "codex") {
			return nil, nil
		}
		s := locateTranscript(row["key"], row["engine"])
		if s == nil {
			return nil, nil
		}
		return cachedTranscript(s.storePath, s.Tool)
	}
	return nil, errors.New("pane unavailable")
}

// Discovery walks engine stores, so share it across peers and polls. A short
// bounded cache also retries absent files without scanning every 500ms.
var transcriptLocations = struct {
	sync.Mutex
	entries map[string]transcriptLocation
}{entries: make(map[string]transcriptLocation)}

type transcriptLocation struct {
	session *aiSession
	checked time.Time
}

func locateTranscript(key, engine string) *aiSession {
	roots := loadSessionRoots()
	cacheKey := engine + ":" + key
	for _, r := range roots {
		cacheKey += "\x00" + r.Home
	}
	transcriptLocations.Lock()
	defer transcriptLocations.Unlock()
	now := time.Now()
	location, ok := transcriptLocations.entries[cacheKey]
	ttl := 30 * time.Second
	if location.session == nil {
		ttl = 2 * time.Second
	}
	if ok && now.Sub(location.checked) < ttl {
		if location.session == nil {
			return nil
		}
		if _, err := os.Stat(location.session.storePath); err == nil {
			return location.session
		}
	}
	var sessions []aiSession
	for _, root := range roots {
		if engine == "claude" {
			sessions = append(sessions, claudeSessions(root.Home)...)
		} else {
			sessions = append(sessions, codexSessions(root.Home)...)
		}
	}
	session := transcriptSession(key, engine, sessions)
	if len(transcriptLocations.entries) >= 8 {
		var oldest string
		for k, l := range transcriptLocations.entries {
			if oldest == "" || l.checked.Before(transcriptLocations.entries[oldest].checked) {
				oldest = k
			}
		}
		delete(transcriptLocations.entries, oldest)
	}
	transcriptLocations.entries[cacheKey] = transcriptLocation{session: session, checked: now}
	return session
}

func transcriptTarget(name string, raw json.RawMessage) string {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	var keys []string
	switch name {
	case "Bash", "shell", "shell_command", "exec_command":
		keys = []string{"command", "cmd"}
	case "Read", "Edit", "Write":
		keys = []string{"file_path", "path"}
	}
	for _, key := range keys {
		var value string
		if json.Unmarshal(args[key], &value) == nil {
			return transcriptCut(value, 512)
		}
		var parts []string
		if json.Unmarshal(args[key], &parts) == nil && len(parts) > 0 {
			return transcriptCut(strings.Join(parts, " "), 512)
		}
	}
	// Preserve argument order for less common tools.
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if _, err := decoder.Token(); err != nil {
		return ""
	}
	for decoder.More() {
		if _, err := decoder.Token(); err != nil {
			break
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			break
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			return transcriptCut(text, 512)
		}
	}

	return ""
}

var transcriptExit = regexp.MustCompile(`(?im)^(?:exit code|process exited with code)[ \t]*:?[ \t]*(-?[0-9]+)`)

func transcriptOutputStatus(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	var output struct {
		ExitCode *int `json:"exit_code"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(s), &output) == nil {
		code := output.ExitCode
		if code == nil {
			code = output.Metadata.ExitCode
		}
		if code != nil {
			if *code != 0 {
				return "error"
			}
			return "ok"
		}
	}
	if m := transcriptExit.FindStringSubmatch(s); len(m) > 1 && m[1] != "0" {
		return "error"
	}
	return "ok"
}

// Scan incrementally in memory: only the last 200 entries and their tool IDs
// survive. Oversized physical lines are refused locally, never sent unbounded.
// Each active file retains a bounded parser window and a complete-line offset.
// Appends read only the suffix; rotation or truncation starts a fresh window.
var transcriptCache = struct {
	sync.Mutex
	files map[string]*transcriptCursor
}{files: make(map[string]*transcriptCursor)}

type transcriptCursor struct {
	info    os.FileInfo
	offset  int64
	entries []transcriptEntry
	used    time.Time
}

func cachedTranscript(path, engine string) ([]transcriptEntry, error) {
	transcriptCache.Lock()
	defer transcriptCache.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	cursor := transcriptCache.files[path]
	if cursor == nil || !os.SameFile(cursor.info, info) || info.Size() < cursor.info.Size() || info.Size() == cursor.info.Size() && !info.ModTime().Equal(cursor.info.ModTime()) {
		cursor = &transcriptCursor{info: info}
	}
	if info.Size() != cursor.offset {
		if _, err = f.Seek(cursor.offset, io.SeekStart); err != nil {
			return nil, err
		}
		entries, consumed, err := parseTranscript(io.LimitReader(f, info.Size()-cursor.offset), engine, cursor.entries)
		if err != nil {
			delete(transcriptCache.files, path)
			return nil, err
		}
		cursor.entries = entries
		cursor.offset += consumed
	}
	cursor.info = info
	cursor.used = time.Now()
	if len(transcriptCache.files) >= 8 && transcriptCache.files[path] == nil {
		var oldest string
		for key, c := range transcriptCache.files {
			if oldest == "" || c.used.Before(transcriptCache.files[oldest].used) {
				oldest = key
			}
		}
		delete(transcriptCache.files, oldest)
	}
	transcriptCache.files[path] = cursor
	return boundTranscript(cursor.entries), nil
}
func boundTranscript(entries []transcriptEntry) []transcriptEntry {
	total := 2
	sizes := make([]int, len(entries))
	for i, e := range entries {
		b, _ := json.Marshal(e)
		sizes[i] = len(b) + 1
		total += sizes[i]
	}
	start := 0
	for total > transcriptSnapshotLimit && start < len(entries) {
		total -= sizes[start]
		start++
	}
	entries = entries[start:]

	if entries == nil {
		return []transcriptEntry{}
	}
	return entries
}
func readTranscript(r io.Reader, engine string) ([]transcriptEntry, error) {
	entries, _, err := parseTranscript(r, engine, nil)
	return boundTranscript(entries), err
}
func parseTranscript(r io.Reader, engine string, entries []transcriptEntry) ([]transcriptEntry, int64, error) {
	var consumed int64

	if engine != "claude" && engine != "codex" {
		return nil, 0, errors.New("no transcript parser")
	}
	if entries == nil {
		entries = make([]transcriptEntry, 0, 200)
	}
	result := func(id, status string) {
		for i := range entries {
			for j := range entries[i].Tools {
				if entries[i].Tools[j].id == id && id != "" {
					entries[i].Tools[j].Status = status
				}
			}
		}
	}
	add := func(e transcriptEntry) {
		e.Text = transcriptCut(e.Text, transcriptTextLimit)
		// Bound escaped JSON too, so one unusual entry cannot evict itself.
		for {
			b, _ := json.Marshal(e)
			if len(b) <= 24*1024 {
				break
			}
			if len(e.Tools) > 0 {
				e.Tools = e.Tools[:len(e.Tools)-1]
				e.Text = transcriptCut(e.Text, 4096) + "\n[tools cut]"
			} else {
				e.Text = transcriptCut(e.Text, 4096)
				break
			}
		}
		if len(entries) == 200 {
			copy(entries, entries[1:])
			entries = entries[:199]
		}
		entries = append(entries, e)
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			consumed += int64(i + 1)
			return i + 1, data[:i], nil
		}
		// A writer may be halfway through an event. Re-read it on the next poll.
		return 0, nil, nil
	})
	for scanner.Scan() {
		var event struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if engine == "claude" {
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(event.Message, &msg) != nil {
				continue
			}
			e := transcriptEntry{Role: msg.Role, Tools: []transcriptTool{}}
			var text string
			if json.Unmarshal(msg.Content, &text) == nil {
				e.Text = text
			} else {
				var blocks []struct {
					Type   string          `json:"type"`
					Text   string          `json:"text"`
					ID     string          `json:"id"`
					Name   string          `json:"name"`
					Input  json.RawMessage `json:"input"`
					ToolID string          `json:"tool_use_id"`
					Error  bool            `json:"is_error"`
				}
				if json.Unmarshal(msg.Content, &blocks) != nil {
					continue
				}
				var texts []string
				toolsCut := false
				for _, b := range blocks {
					switch b.Type {
					case "text":
						texts = append(texts, b.Text)
					case "tool_use":
						if len(e.Tools) < 32 {
							e.Tools = append(e.Tools, transcriptTool{Name: transcriptCut(b.Name, 128), Target: transcriptTarget(b.Name, b.Input), Status: "running", id: transcriptToolID(b.ID)})
						} else {
							toolsCut = true
						}
					case "tool_result":
						status := "ok"
						if b.Error {
							status = "error"
						}
						result(b.ToolID, status)
					}
				}
				e.Text = strings.Join(texts, "\n")
				if toolsCut {
					e.Text += "\n[tools cut]"
				}
			}
			if (e.Role == "user" || e.Role == "assistant") && (e.Text != "" || len(e.Tools) > 0) {
				add(e)
			}
		} else if event.Type == "response_item" {
			var item struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Name      string          `json:"name"`
				Arguments string          `json:"arguments"`
				CallID    string          `json:"call_id"`
				Output    json.RawMessage `json:"output"`
			}
			if json.Unmarshal(event.Payload, &item) != nil {
				continue
			}
			switch item.Type {
			case "message":
				if item.Role == "user" || item.Role == "assistant" {
					var texts []string
					for _, b := range item.Content {
						if b.Type == "input_text" || b.Type == "output_text" || b.Type == "text" {
							texts = append(texts, b.Text)
						}
					}
					if len(texts) > 0 {
						add(transcriptEntry{Role: item.Role, Text: strings.Join(texts, "\n"), Tools: []transcriptTool{}})
					}
				}
			case "function_call":
				add(transcriptEntry{Role: "assistant", Tools: []transcriptTool{{Name: transcriptCut(item.Name, 128), Target: transcriptTarget(item.Name, json.RawMessage(item.Arguments)), Status: "running", id: transcriptToolID(item.CallID)}}})
			case "function_call_output":
				result(item.CallID, transcriptOutputStatus(item.Output))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, consumed, err
	}
	return entries, consumed, nil
}
