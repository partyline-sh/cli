package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestContractorEntry(t *testing.T) {
	log := fixtureFake(t)
	now := time.Now()
	first := fakeContractor(t, contractor{ID: "aa0001", Loop: "abcdef", State: "running", Started: now})
	second := fakeContractor(t, contractor{ID: "bb0002", Loop: "abcdef", State: "running", Started: now.Add(-time.Minute)})
	act := tabActions{rows: func(time.Time) []tabRow { return nil }, now: func() time.Time { return now }}
	for _, tc := range []struct{ arg, want string }{
		{second.ID, second.ID}, {"ffffff", first.ID}, {"../../bb0002", first.ID}, {"", first.ID},
	} {
		m := contractorTabArgs(newTabModel(act), []string{"--contractor", tc.arg})
		if !m.contractors || m.preview == nil || m.preview.c.ID != tc.want {
			t.Fatalf("%q: wrong tab/selection: %+v", tc.arg, m)
		}
	}
	bare := contractorTabArgs(newTabModel(act), []string{"--contractor"})
	if !bare.contractors || bare.preview == nil || bare.preview.c.ID != first.ID {
		t.Fatal("bare flag did not select first contractor")
	}
	if contractorTabArgs(newTabModel(act), nil).contractors {
		t.Fatal("default entry left Board")
	}
	for _, section := range []tabSection{tabInProgress, tabInReview} {
		m := newTabModel(tabActions{rows: func(time.Time) []tabRow { return []tabRow{{Section: section, Watch: second.ID}} }, now: act.now})
		m.col = section
		m.selectCard()
		m = tabKey(m, "enter")
		if !m.contractors || m.preview == nil || m.preview.c.ID != second.ID {
			t.Fatal("Enter did not select contractor", section)
		}
	}
	for _, id := range []string{second.ID, "ffffff", "../../bb0002"} {
		args := loopsPopupCommand("ptln", 120, 38, "--contractor", id)
		if !strings.Contains(strings.Join(args, " "), "loop tab --contractor") {
			t.Fatal(args)
		}
		if id == "../../bb0002" && strings.Contains(strings.Join(args, " "), id) {
			t.Fatal("malformed id forwarded", args)
		}
	}
	tmuxCmdMain([]string{"--contractor-click", second.ID})
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "loop tab --contractor "+second.ID) {
		t.Fatalf("click command: %s", b)
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	tmuxCmdMain([]string{"--contractor-click", "../../bb0002"})
	b, _ = os.ReadFile(log)
	if strings.Contains(string(b), "display-popup") {
		t.Fatalf("malformed click ran tmux: %s", b)
	}
	found := false
	for _, item := range tmuxCommandItems("ptln") {
		if item.key == "k" {
			found = true
			if !strings.Contains(strings.Join(item.run, " "), "tmux --loops-tab --contractor") {
				t.Fatal(item.run)
			}
		}
	}
	if !found {
		t.Fatal("missing k command")
	}
	tmuxCmdMain([]string{"--loops-tab", "--contractor"})
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "loop tab --contractor") || strings.Contains(string(b), "--contractors-popup") {
		t.Fatalf("keyboard command: %s", b)
	}
}
