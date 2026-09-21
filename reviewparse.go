// G.5 — reading a reviewer's reply as DATA rather than as a verdict line.
//
// The single-lane contract was "end your reply with VERDICT: PASS or VERDICT: FAIL — reason". That
// is enough for one reviewer and useless for two: merging findings across lanes needs each finding
// to have a file, a line and a title, and prose cannot be joined on anything.
//
// So the reviewer is now asked for a fenced JSON block. The VERDICT line is KEPT as a fallback,
// which matters more than it looks: a smaller or older model asked for JSON will sometimes produce
// prose anyway, and the difference between "degrades to one-lane behaviour" and "the gate stops
// working for that project" is exactly this fallback. Fail-closed still applies underneath both —
// a reply we cannot read at all is a rejection, never a pass.
package main

import (
	"encoding/json"
	"strings"

	"partyline.sh/partyline/internal/gate"
)

// reviewReply is what a reviewer lane produces.
type reviewReply struct {
	Pass     bool
	Findings []gate.Finding
	// Structured reports whether the findings came from JSON. False means we fell back to the
	// verdict line and have a pass/fail but no mergeable findings — worth recording, because a
	// project whose reviewer never produces structured output gets no agreement signal and should
	// be able to see why.
	Structured bool
	Reason     string
}

// jsonFindings is the wire shape asked of the reviewer. Deliberately small: every field a model has
// to produce is a field it can get wrong, and file/line/title is the minimum that supports merging.
type jsonFindings struct {
	Verdict  string `json:"verdict"`
	Findings []struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Title    string `json:"title"`
		Body     string `json:"body"`
		Severity string `json:"severity"`
	} `json:"findings"`
}

// parseReviewReply reads a lane's reply. Order matters: JSON first because it carries more, the
// verdict line second because it always works, and fail-closed last because an unreadable answer
// must never become a pass.
func parseReviewReply(reply string) reviewReply {
	if j, ok := extractReviewJSON(reply); ok {
		out := reviewReply{Structured: true, Pass: strings.EqualFold(j.Verdict, "pass")}
		for _, f := range j.Findings {
			t := strings.TrimSpace(f.Title)
			if t == "" {
				continue // a finding with no title cannot be merged or shown; drop it rather than guess
			}
			out.Findings = append(out.Findings, gate.Finding{
				File:     strings.TrimSpace(f.File),
				Line:     f.Line,
				Title:    t,
				Body:     gate.Truncate(strings.TrimSpace(f.Body), 2000),
				Severity: strings.ToLower(strings.TrimSpace(f.Severity)),
			})
		}
		if !out.Pass {
			out.Reason = "reviewer: rejected"
			if len(out.Findings) > 0 {
				out.Reason = "reviewer: " + out.Findings[0].Title
			}
		}
		return out
	}
	// No usable JSON — fall back to the line contract that has always worked.
	pass, reasons := parseReviewVerdict(reply)
	return reviewReply{Pass: pass, Reason: reasons}
}

// extractReviewJSON finds the reviewer's JSON block.
//
// Models fence it inconsistently — ```json, bare ```, or no fence at all with prose either side —
// so this scans for the LAST balanced object containing a "verdict" key rather than trusting a
// fence. Last, not first: a model that reasons out loud will often show an example object before
// committing to its actual answer, and taking the first would read the example as the verdict.
func extractReviewJSON(reply string) (jsonFindings, bool) {
	var best jsonFindings
	found := false
	for i := 0; i < len(reply); i++ {
		if reply[i] != '{' {
			continue
		}
		end, ok := matchBrace(reply, i)
		if !ok {
			continue
		}
		var j jsonFindings
		if err := json.Unmarshal([]byte(reply[i:end+1]), &j); err == nil && j.Verdict != "" {
			best, found = j, true
		}
		// Keep scanning: the LAST valid object wins.
	}
	return best, found
}

// matchBrace returns the index of the '}' closing the object opened at start, ignoring braces
// inside strings so a finding body containing one cannot end the object early.
func matchBrace(s string, start int) (int, bool) {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// braces inside a string are content, not structure
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// reviewerJSONInstruction is appended to the reviewer prompt. It asks for the structured block and
// KEEPS the verdict line, so a model that ignores the JSON request still produces a readable
// answer rather than an unparseable one that fails the task closed.
const reviewerJSONInstruction = `
Report your findings as a JSON object in a fenced code block, then the verdict line.

` + "```json" + `
{"verdict": "pass" | "fail",
 "findings": [
   {"file": "path/to/file.go", "line": 42,
    "title": "one short line naming the problem",
    "body": "why it is wrong and what it breaks",
    "severity": "high" | "medium" | "low"}
 ]}
` + "```" + `

Rules for the findings list:
  • One entry per DISTINCT problem. Do not split one problem across entries.
  • title is a short noun phrase, not a sentence — another reviewer describing the SAME problem
    should produce a similar title, because identical findings from independent reviewers are
    merged and shown as agreement.
  • line is where the problem IS, not where you would fix it.
  • findings may be empty on a pass. A pass with findings means "merge it, but these are worth
    knowing"; a fail means the change does not satisfy the task.

Then end your reply with EXACTLY one line:
VERDICT: PASS
or
VERDICT: FAIL — <one-line reason>
`
