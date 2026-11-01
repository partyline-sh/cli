package main

import (
	"fmt"
	"regexp"
	"strings"
)

// The brief and the ready report are markdown with required sections. They are checked for
// presence and substance, not judged: whether the claims hold is the reviewers' job.

var loopBriefSections = []string{"Goal", "Decisions", "Constraints", "Out of scope", "Acceptance", "Verification", "Unknowns"}

// loopReportSections are what `ready` requires. Evidence is level 5 of the quality standard; the
// other three are level 6 and the three questions.
var loopReportSections = []string{"Evidence", "Hardest decision", "Rejected alternatives", "Least confident", "What an excellent reviewer would criticise"}

const loopBriefTemplate = `# <title>

## Goal
What the user will be able to do, in their terms.

## Decisions
What was decided in the conversation, each with its reason.

## Constraints
What must not change; interfaces others depend on.

## Out of scope
What this run will not do.

## Acceptance
One per line. ` + "`run: <command>`" + ` must exit 0; ` + "`file: <path>`" + ` must exist.

## Verification
For each acceptance item, the check that proves it.

## Unknowns
Open questions, not assumptions. "None" if there are none.
`

const loopReportTemplate = `## Evidence
Each claim about the work, and what confirms it: a command and its result, a file and line, a doc
URL. Mark anything not confirmed as inferred.

## Hardest decision

## Rejected alternatives
And why each was rejected.

## Least confident

## What an excellent reviewer would criticise
And what you did about it.
`

var mdHeading = regexp.MustCompile(`(?m)^#{1,3}\s+(.+?)\s*$`)

// mdSections maps each heading (lowercased) to the text under it.
func mdSections(doc string) map[string]string {
	out := map[string]string{}
	idx := mdHeading.FindAllStringSubmatchIndex(doc, -1)
	for i, m := range idx {
		end := len(doc)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out[strings.ToLower(doc[m[2]:m[3]])] = strings.TrimSpace(doc[m[1]:end])
	}
	return out
}

// checkSections reports the required sections that are missing or empty.
func checkSections(doc string, required []string, what, template string) error {
	got := mdSections(doc)
	var missing []string
	for _, s := range required {
		body, ok := got[strings.ToLower(s)]
		if !ok || len(strings.Fields(body)) < 1 {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("the %s is missing %s. Required sections:\n\n%s", what, strings.Join(missing, ", "), template)
}

func validateBrief(doc string) error {
	if err := checkSections(doc, loopBriefSections, "brief", loopBriefTemplate); err != nil {
		return err
	}
	if len(briefAcceptance(doc)) == 0 {
		return fmt.Errorf("the brief's Acceptance section has no `run:` or `file:` lines")
	}
	return nil
}

func validateReport(doc string) error {
	return checkSections(doc, loopReportSections, "report", loopReportTemplate)
}

type loopAcceptance struct{ Kind, Value string } // run | file

// briefAcceptance reads the structured lines of the Acceptance section.
func briefAcceptance(doc string) []loopAcceptance {
	var out []loopAcceptance
	for _, line := range strings.Split(mdSections(doc)["acceptance"], "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*"))
		line = strings.Trim(line, "` ")
		for _, kind := range []string{"run", "file"} {
			if v, ok := strings.CutPrefix(line, kind+":"); ok && strings.TrimSpace(v) != "" {
				out = append(out, loopAcceptance{kind, strings.Trim(strings.TrimSpace(v), "`")})
			}
		}
	}
	return out
}
