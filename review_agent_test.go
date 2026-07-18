package main

import "testing"

// parseGradedReview must fail CLOSED — an unparseable, empty, or invalid-grade reply yields an error so
// the review run fails loudly (retryable) rather than recording a misleading grade (mirrors
// parseReviewVerdict in verify_test.go).
func TestParseGradedReview(t *testing.T) {
	cases := []struct {
		name      string
		reply     string
		wantErr   bool
		wantGrade string
		wantIssue int
	}{
		{
			name:      "fenced json",
			reply:     "here's my review:\n```json\n{\"grade\":\"B\",\"summary\":\"solid\",\"issues\":[{\"severity\":\"low\",\"text\":\"nit\"}]}\n```\nthanks",
			wantGrade: "B",
			wantIssue: 1,
		},
		{
			name:      "bare json, no fence",
			reply:     "{\"grade\":\"a\",\"summary\":\"great\",\"issues\":[]}",
			wantGrade: "A", // normalized to upper
		},
		{
			name:      "grade with surrounding prose and objects",
			reply:     "The task { was } to do X. My verdict:\n{\"grade\":\"F\",\"summary\":\"broken\",\"issues\":[{\"severity\":\"high\",\"text\":\"crashes\"},{\"severity\":\"med\",\"text\":\"no tests\"}]}",
			wantGrade: "F",
			wantIssue: 2,
		},
		{name: "no json at all", reply: "I think it looks pretty good overall, B+ maybe", wantErr: true},
		{name: "empty", reply: "", wantErr: true},
		{name: "invalid grade E", reply: "{\"grade\":\"E\",\"summary\":\"x\"}", wantErr: true},
		{name: "invalid grade word", reply: "{\"grade\":\"good\",\"summary\":\"x\"}", wantErr: true},
		{name: "malformed json", reply: "```json\n{\"grade\":\"A\", oops}\n```", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			grade, _, issues, err := parseGradedReview(c.reply)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got grade=%q", grade)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if grade != c.wantGrade {
				t.Errorf("grade = %q, want %q", grade, c.wantGrade)
			}
			if len(issues) != c.wantIssue {
				t.Errorf("issues = %d, want %d", len(issues), c.wantIssue)
			}
		})
	}
}
