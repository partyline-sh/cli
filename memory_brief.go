package main

// THE BRIEF: what a session is told before anyone types.
//
// This is the whole point of the system. Recording facts is worthless if reading them is a tool
// call an agent has to choose to make — the previous design proved that, with months of use and
// 46 recorded facts nobody's session ever read back. So the brief is PUSHED: injected at session
// start, in the agent's context, whether or not it thinks to ask.
//
// It has to stay short. A brief that dumps everything is noise, gets skimmed, and teaches people
// to ignore it — the failure mode that kills memory systems. So: the whole project's decisions
// and constraints are eligible, but what is ABOUT THIS REPO comes first, and the cap is hard.

import (
	"fmt"
	"sort"
	"strings"
)

// briefMax is how many facts a session start may carry. Chosen to fit on a screen: past this
// people stop reading, and an unread brief is worse than none because it looks like coverage.
const briefMax = 12

// rankFacts orders what matters to a session in THIS repo: the repo's own facts first, then
// project-wide ones, newest first within each, with questions surfaced above settled things
// because an open question is the one item that needs a human.
func rankFacts(all []fact, repo string) []fact {
	out := append([]fact(nil), all...)
	weight := func(f fact) int {
		w := 0
		if f.Repo == repo && repo != "" {
			w -= 100 // this repo
		} else if f.Repo == "" {
			w -= 50 // whole project
		}
		if f.Kind == "question" {
			w -= 25 // unanswered things need a person
		}
		// A proposed fact was inferred from a source, not written by someone who was there. It
		// still belongs in the brief, but under the facts a person chose to record: if the two
		// are indistinguishable, the memory's credibility settles at the level of its weakest
		// entry, and a brief people stop trusting is worse than no brief.
		if f.By == proposedBy {
			w += 40
		}
		return w
	}
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := weight(out[i]), weight(out[j])
		if wi != wj {
			return wi < wj
		}
		return out[i].At.After(out[j].At)
	})
	return out
}

// conflicts finds facts that disagree: same kind, same repo scope, overlapping tags, neither
// superseding the other. Surfacing them is the point — two people's agents holding opposite
// beliefs is exactly the silent failure that sends humans back to pasting into Slack.
func conflicts(facts []fact) [][2]fact {
	var out [][2]fact
	for i := range facts {
		for j := i + 1; j < len(facts); j++ {
			a, b := facts[i], facts[j]
			// Scopes OVERLAP rather than match: a project-wide decision and a repo-scoped one
			// can absolutely contradict each other — in the first live test that was exactly the
			// pair ("we use gRPC" project-wide vs "we use REST" in integration) and requiring
			// equal scope hid it, which is the failure this function exists to prevent.
			if a.Kind != b.Kind || a.By == b.By || !scopesOverlap(a.Repo, b.Repo) {
				continue
			}
			if len(a.Tags) == 0 || !sharesTag(a.Tags, b.Tags) {
				continue
			}
			out = append(out, [2]fact{a, b})
		}
	}
	return out
}

// scopesOverlap: two facts can be about the same thing when they name the same repo, or when
// either is about the whole project.
func scopesOverlap(a, b string) bool { return a == b || a == "" || b == "" }

func sharesTag(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// renderBrief is the text a session starts with. Plain, short, and it says where it came from so
// a reader can go check — a brief nobody can audit is a brief nobody should trust.
// renderBrief renders the ranked facts. max caps how many are shown; 0 means ALL.
//
// The cap exists for the SESSION-START brief, which has to fit on a screen. It is wrong for the
// project_memory tool: an agent that deliberately asked to read the memory was handed 12 of 27
// and told to run a shell command for the rest, which an agent working through MCP cannot do.
func renderBrief(label, repo string, facts []fact, max int) string {
	ranked := rankFacts(facts, repo)
	if len(ranked) == 0 {
		return ""
	}
	shown := ranked
	if max > 0 && len(shown) > max {
		shown = shown[:max]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "☎ %s — what the team has learned", label)
	if repo != "" {
		fmt.Fprintf(&b, " (you are in %s)", repo)
	}
	b.WriteString("\n\n")
	for _, f := range shown {
		scope := "project"
		if f.Repo != "" {
			scope = f.Repo
		}
		fmt.Fprintf(&b, "- [%s · %s] %s\n", f.Kind, scope, oneLine(f.Body))
		fmt.Fprintf(&b, "  %s, %s · %s\n", f.By, humanAge(f.At), f.ID)
	}
	if n := len(ranked) - len(shown); n > 0 {
		fmt.Fprintf(&b, "\n%d more: `ptln memory ls`\n", n)
	}
	for _, c := range conflicts(shown) {
		fmt.Fprintf(&b, "\n⚠ these disagree — settle it before relying on either:\n  %s (%s): %s\n  %s (%s): %s\n",
			c[0].By, c[0].ID, oneLine(c[0].Body), c[1].By, c[1].ID, oneLine(c[1].Body))
	}
	return b.String()
}
