package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// plan_cmds.go — `ptln plan`: see and discard the planning drafts on this machine.
//
// Drafts are per-thread files under ~/.partyline/planning/. Before this they were reachable ONLY
// through the MCP tools, which can open one and finalize one but cannot LIST or DELETE one — so a
// draft orphaned by a failed finalize was invisible and permanent from the terminal, and the board
// went on asking for answers the user had already given. A session that cannot clean up after
// itself makes a mess by design.

type planListing struct {
	Thread   string
	Draft    *planDraft
	Modified time.Time
	Corrupt  bool
	Path     string
}

// listDrafts reads every draft on this machine, newest first. A corrupt file is REPORTED rather
// than skipped: it is exactly the thing you need to be able to see in order to delete it.
func listDrafts() []planListing {
	dir := filepath.Join(daemonDir(), "planning")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []planListing
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		thread := strings.TrimSuffix(e.Name(), ".json")
		p := filepath.Join(dir, e.Name())
		l := planListing{Thread: thread, Path: p}
		if info, ierr := e.Info(); ierr == nil {
			l.Modified = info.ModTime()
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			l.Corrupt = true
		} else {
			var d planDraft
			if json.Unmarshal(b, &d) != nil {
				l.Corrupt = true
			} else {
				l.Draft = &d
			}
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// isPlanDraftSub reports whether `ptln plan <arg>` is addressing the DRAFT store rather than
// starting a planning conversation. Kept as an explicit allowlist so a plain idea can never be
// swallowed as a subcommand: `ptln plan "list the open bugs"` must still open the agent.
func isPlanDraftSub(arg string) bool {
	switch arg {
	case "ls", "list", "show", "rm", "discard", "delete", "drafts":
		return true
	}
	return false
}

func planCmdMain(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "ls", "list":
		planList()
	case "show":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ptln plan show <thread-id>"))
		}
		planShow(args[1])
	case "drafts":
		planList()
	case "rm", "discard", "delete":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ptln plan rm <thread-id>   (or `ptln plan rm --all`)"))
		}
		planRemove(args[1:])
	case "-h", "--help", "help":
		fmt.Println("ptln plan — the planning drafts on this machine")
		fmt.Println("  ptln plan                    list them (thread, title, what's unanswered)")
		fmt.Println("  ptln plan show <thread-id>   the full draft: slots, questions, assumptions")
		fmt.Println("  ptln plan rm <thread-id>     discard one (the filed work is untouched)")
		fmt.Println("  ptln plan rm --all           discard every draft on this machine")
		fmt.Println()
		fmt.Println("  A draft is local planning state. Discarding one never deletes filed work items —")
		fmt.Println("  it only ends the in-progress interview, so planning_open starts clean.")
	default:
		fatal(fmt.Errorf("unknown: ptln plan %s   (try `ptln plan --help`)", sub))
	}
}

func planList() {
	drafts := listDrafts()
	if len(drafts) == 0 {
		fmt.Println("No planning drafts on this machine.")
		fmt.Println("  A draft appears when an LLM session calls planning_open, and clears when it files.")
		return
	}
	fmt.Printf("%d planning draft(s) on this machine:\n\n", len(drafts))
	for _, l := range drafts {
		if l.Corrupt {
			fmt.Printf("  %s  (unreadable)\n", shortID(l.Thread))
			fmt.Printf("      → ptln plan rm %s\n", l.Thread)
			continue
		}
		title := strings.TrimSpace(l.Draft.Title)
		if title == "" {
			title = strings.TrimSpace(l.Draft.Idea)
		}
		if title == "" {
			title = "(untitled)"
		}
		if len(title) > 56 {
			title = title[:55] + "…"
		}
		fmt.Printf("  %s  %s\n", shortID(l.Thread), title)
		if n := len(l.Draft.unanswered()); n > 0 {
			fmt.Printf("      %d unanswered question(s) — the session is waiting on you\n", n)
		}
		fmt.Printf("      thread %s · %s\n", l.Thread, humanAge(l.Modified))
	}
	fmt.Println()
	fmt.Println("Discard one with `ptln plan rm <thread-id>`. Filed work items are never touched.")
}

func planShow(thread string) {
	d := loadDraft(thread)
	if d == nil {
		fmt.Printf("No draft for thread %s on this machine.\n", thread)
		fmt.Println("  → ptln plan   to see the drafts that do exist")
		return
	}
	title := strings.TrimSpace(d.Title)
	if title == "" {
		title = "(untitled)"
	}
	fmt.Printf("%s  (%s)\n", title, kindOrTask(d.Kind))
	fmt.Printf("thread %s\n\n", d.Thread)
	if strings.TrimSpace(d.Idea) != "" {
		fmt.Printf("Idea:\n  %s\n\n", strings.TrimSpace(d.Idea))
	}
	if len(d.OpenQuestions) > 0 {
		fmt.Println("Open questions:")
		for i, q := range d.OpenQuestions {
			mark := "?"
			if q.answered() {
				mark = "✓"
			}
			fmt.Printf("  %s %d. %s\n", mark, i+1, q.Text)
			if q.answered() {
				fmt.Printf("       %s\n", q.Answer)
			}
		}
		fmt.Println()
	}
	if len(d.Assumptions) > 0 {
		fmt.Println("Assumptions:")
		for _, a := range d.Assumptions {
			fmt.Printf("  · %s\n", a)
		}
		fmt.Println()
	}
	if strings.TrimSpace(d.Document) != "" {
		fmt.Printf("Document (%d chars):\n%s\n", len(d.Document), d.Document)
	}
	fmt.Printf("\nDiscard it with `ptln plan rm %s`.\n", d.Thread)
}

func planRemove(args []string) {
	if args[0] == "--all" {
		drafts := listDrafts()
		if len(drafts) == 0 {
			fmt.Println("No planning drafts to discard.")
			return
		}
		for _, l := range drafts {
			clearDraft(l.Thread)
		}
		fmt.Printf("Discarded %d draft(s). Filed work items are untouched.\n", len(drafts))
		return
	}
	thread := args[0]
	if loadDraft(thread) == nil {
		// Still attempt the remove: an UNREADABLE draft does not load but must still be deletable,
		// which is the whole reason someone reaches for this command.
		if _, err := os.Stat(planPath(thread)); err != nil {
			fmt.Printf("No draft for thread %s on this machine.\n", thread)
			fmt.Println("  → ptln plan   to see the drafts that do exist")
			return
		}
	}
	clearDraft(thread)
	fmt.Printf("Discarded the draft for thread %s. Filed work items are untouched.\n", shortID(thread))
}
