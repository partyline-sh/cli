package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func recallFixture() string {
	var out bytes.Buffer
	for i := 1; i <= 50; i++ {
		name, from := "#11 claude", "claude-session"
		if i%2 == 0 {
			name, from = "you", "you"
		}
		_ = json.NewEncoder(&out).Encode(chatMessage{Seq: i, At: time.Date(2026, 11, i/10+1, 12, 0, 0, 0, time.UTC), Name: name, From: from, Text: "message about uploads"})
	}
	return out.String()
}

func TestChatRecallPagingAndFilters(t *testing.T) {
	for _, tc := range []struct {
		args               []string
		first, last, count int
	}{
		{nil, 31, 50, 20},
		{[]string{"--before", "31"}, 11, 30, 20},
		{[]string{"--after", "20", "--limit", "3"}, 21, 23, 3},
		{[]string{"--around", "25", "--limit", "5"}, 23, 27, 5},
		{[]string{"--sender", "#11", "--limit", "2"}, 47, 49, 2},
		{[]string{"--sender", "you", "--limit", "2"}, 48, 50, 2},
		{[]string{"--since", "2026-11-03", "--until", "2026-11-03", "--limit", "100"}, 20, 29, 10},
		{[]string{"--search", "UPLOADS", "--limit", "1"}, 50, 50, 1},
	} {
		o, err := parseChatRecall(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		page, err := readChatRecall(strings.NewReader(recallFixture()), o)
		if err != nil || len(page) != tc.count || page[0].Seq != tc.first || page[len(page)-1].Seq != tc.last {
			t.Fatalf("%v: %v (%v)", tc.args, page, err)
		}
	}
	for _, args := range [][]string{{"--limit", "101"}, {"--limit", "0"}, {"--before"}, {"--bad", "x"}, {"--since", "bad"}, {"--since", "2026-11-04", "--until", "2026-11-03"}, {"--around", "2", "--after", "1"}} {
		if _, err := parseChatRecall(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestChatRecallRedactionBeforeSearch(t *testing.T) {
	canaries := []string{"sk_abcdefghijklmnop123456789", "AQ.abcdefghijklmnopqrstuvwxyz12345", "ghp_abcdefghijklmnop123456789", "HumanSecret123"}
	message := chatMessage{Seq: 1, From: "you", Name: "you", Text: strings.Join(canaries[:3], "\n") + "\nMini password is " + canaries[3] + "\nPassword:\nAnotherSecret456\n-----BEGIN PRIVATE KEY-----\nPRIVATE_CONTENT\n-----END PRIVATE KEY-----\nkeep useful context"}
	var wire bytes.Buffer
	_ = json.NewEncoder(&wire).Encode(message)
	o, _ := parseChatRecall(nil)
	page, err := readChatRecall(bytes.NewReader(wire.Bytes()), o)
	if err != nil || len(page) != 1 {
		t.Fatal(err)
	}
	for _, secret := range append(canaries, "AnotherSecret456", "PRIVATE_CONTENT") {
		if strings.Contains(page[0].Text, secret) {
			t.Fatalf("exposed canary %q", secret)
		}
	}
	if !strings.Contains(page[0].Text, "keep useful context") {
		t.Fatal("lost non-secret text")
	}
	o.search = "humansecret"
	page, err = readChatRecall(bytes.NewReader(wire.Bytes()), o)
	if err != nil || len(page) != 0 {
		t.Fatal("search revealed redacted content")
	}
}

func TestChatRecallDoesNotConsumeUnreadOrWriteRoom(t *testing.T) {
	chatTestRoom(t, chatMember{Key: "agent", Name: "#11 claude", Read: 0})
	post(t, chatMessage{From: "you", Name: "you", Text: "recall this"})
	before, err := os.ReadFile(chatRoomPath())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := chatRecall([]string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var page []chatMessage
	if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page) != 1 || page[0].Seq != 1 {
		t.Fatalf("invalid JSON recall: %s, %v", out.String(), err)
	}
	after, err := os.ReadFile(chatRoomPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recall changed unread state")
	}
}

func TestChatRecallBoundsLongMessages(t *testing.T) {
	page := make([]chatMessage, 100)
	for i := range page {
		page[i] = chatMessage{Seq: i + 1, Text: strings.Repeat("界", 6000) + "needle"}
	}
	boundChatRecall(page, "needle")
	total := 0
	for _, m := range page {
		total += len(m.Text)
		if !strings.Contains(m.Text, "[excerpt;") {
			t.Fatal("missing truncation marker")
		}
	}
	if total > 80<<10 {
		t.Fatalf("history output too large: %d", total)
	}
	if !strings.Contains(page[0].Text, "needle") {
		t.Fatal("search excerpt lost the match")
	}
}

func TestChatRecallExcludesUnlabelledSecretsBeforeSearch(t *testing.T) {
	chatTestRoom(t)
	post(t, chatMessage{From: "you", Name: "you", Text: "UnlabelledCanary123"})
	original, err := os.ReadFile(chatLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chatRecallExclusionsPath(), []byte("[1]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := chatRecall([]string{"--around", "1", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "UnlabelledCanary") || !strings.Contains(out.String(), "withheld") {
		t.Fatal("exclusion did not hide message")
	}
	out.Reset()
	if err := chatRecall([]string{"--search", "UnlabelledCanary"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No matching messages") {
		t.Fatal("search matched excluded secret")
	}
	after, err := os.ReadFile(chatLogPath())
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("exclusion rewrote original log")
	}
	if err := os.WriteFile(chatRecallExclusionsPath(), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := chatRecall(nil, &out); err == nil {
		t.Fatal("invalid exclusions failed open")
	}
}
