package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// popupTarget supplies a focused AI session without contacting tmux.
type popupTarget struct {
	tmuxSessionTarget
	dir string
}

// ActiveLaunch identifies the scratch session used by popup flows.
func (p *popupTarget) ActiveLaunch() ([]string, string, string, string, bool) {
	return []string{"claude"}, p.dir, "session", "session", true
}

// testSessionWorktreeFlows checks dispatch and cancellation against a scratch repository.
func testSessionWorktreeFlows(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	wt := filepath.Join(t.TempDir(), "feature")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "feature", wt).CombinedOutput(); err != nil {
		t.Fatalf("git worktree: %v: %s", err, out)
	}
	for _, tc := range []struct {
		key   string
		kind  formKind
		title string
	}{{"f", formAsk, "Fork into worktree"}, {"l", formNote, "Worktrees"}, {"r", formPick, "Remove a worktree"}} {
		t.Run("flow-"+tc.key, func(t *testing.T) {
			calls := 0
			target := &popupTarget{dir: repo}
			popupProgram(t, func(m formModel) formModel {
				calls++
				if calls == 1 {
					m, _ = popupPress(m, tc.key)
					return m
				}
				if m.kind != tc.kind || m.title != tc.title {
					t.Fatalf("%s opened kind %d %s", tc.key, m.kind, m.title)
				}
				m, _ = popupPress(m, "esc")
				return m
			})
			wtMenu(target)
			if calls != 2 {
				t.Fatalf("opened %d forms", calls)
			}
			if target.pendingOpen != nil {
				t.Fatal("cancelled worktree flow queued a session")
			}
			if _, err := os.Stat(wt); err != nil {
				t.Fatal("cancelled removal deleted worktree")
			}
		})
	}
	t.Run("remove-no", func(t *testing.T) {
		calls := 0
		popupProgram(t, func(m formModel) formModel {
			calls++
			k := "1"
			if m.kind == formConfirm {
				k = "n"
			}
			m, _ = popupPress(m, k)
			return m
		})
		wtRemoveFlow(repo)
		if calls != 2 {
			t.Fatalf("opened %d forms", calls)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Fatal("no deleted the worktree")
		}
	})
}
