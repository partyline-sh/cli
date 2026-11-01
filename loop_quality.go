package main

// The quality standard every loop prompt carries. It ships in the binary so a loop does not depend
// on a file in anyone's home directory. Levels 1, 3 and 4 are also enforced by partyline itself
// (docs/plans/partyline-loops.md §6); this text is what the models are held to for the rest.
const loopQualityStandard = `Quality standard. Work is held to six levels:
1. Exists: the artifact is on disk.
2. Non-trivial: real content; no placeholders, stubs or TODOs standing in for the work.
3. Structurally valid: it parses, builds, imports.
4. Functionally correct: tests pass; the behaviour can be demonstrated.
5. Verified against reality: claims are checked against primary sources (the code, a command run,
   the official documentation), not assumed. Statements are claims until something confirms them.
6. Uncommon standard: what an excellent practitioner in this domain would criticise has been found
   and addressed.
Separate what was verified from what was inferred. Write an unknown as a question, never a guess.`

// loopReviewRubric is what a reviewer checks, in order.
const loopReviewRubric = `Review, in this order:
1. Does the change do what the brief says? (If there is no brief, what the diff evidently intends.)
2. Correctness: logic errors, unhandled errors, edge cases, races, resource leaks.
3. Security: injection, path traversal, secrets, unsafe defaults, trust boundaries.
4. Claims in the author's report that the code does not support.
5. Tests that would still pass if the feature were broken, and behaviour with no test at all.
6. Levels 2, 5 and 6 of the quality standard.

Priorities:
- P1: must be fixed before merge. Wrong behaviour, data loss, a security hole, a broken build or test.
- P2: should be fixed. A real defect with limited impact, a missing test for real behaviour.
- P3: worth doing. Clarity, small simplifications.
Report only what you have checked in the files. Every finding quotes the exact code it is about.
Do not report style preferences as P1 or P2.`

// loopReviewFormat is the reply format. The reply is parsed, and every quote is checked against
// the file and line it names; a finding whose quote is not there is dropped.
const loopReviewFormat = `Reply with one JSON object and nothing else:
{
  "findings": [
    {"priority": "P1|P2|P3",
     "file": "path relative to the repository root",
     "line": 123,
     "quote": "the exact text of that line or a line within 3 of it, copied from the file",
     "claim": "what is wrong, in one or two sentences",
     "fix": "what to change"}
  ],
  "unverified_claims": ["claims in the author's report with no evidence shown"],
  "verdict": "approve | changes_required"
}
An empty findings list with "approve" is a valid answer.`
