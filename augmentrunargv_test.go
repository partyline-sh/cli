package main

import (
	"reflect"
	"testing"

	"partyline.sh/partyline/internal/api"
)

func TestAugmentRunArgv(t *testing.T) {
	const validID = "12345678-1234-1234-1234-123456789abc"
	base := []string{"crank"}

	tests := []struct {
		name    string
		ev      api.RunEvent
		want    []string
		wantErr bool
	}{
		{
			name: "minimal always appends --run and --resume",
			ev:   api.RunEvent{RunID: validID},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "max-tokens appended when positive",
			ev:   api.RunEvent{RunID: validID, MaxTokens: 5000},
			want: []string{"crank", "--run", validID, "--resume", "--max-tokens", "5000"},
		},
		{
			name: "max-tokens omitted when zero",
			ev:   api.RunEvent{RunID: validID, MaxTokens: 0},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "max-tokens omitted when negative",
			ev:   api.RunEvent{RunID: validID, MaxTokens: -1},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "max-repairs appended when in range",
			ev:   api.RunEvent{RunID: validID, MaxRepairRounds: intp(0)},
			want: []string{"crank", "--run", validID, "--resume", "--max-repairs", "0"},
		},
		{
			name: "max-repairs at the ceiling appended",
			ev:   api.RunEvent{RunID: validID, MaxRepairRounds: intp(maxRepairRoundsCeiling)},
			want: []string{"crank", "--run", validID, "--resume", "--max-repairs", "5"},
		},
		{
			name: "max-repairs omitted when absent (NULL column = crank's default)",
			ev:   api.RunEvent{RunID: validID},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "max-repairs omitted when negative",
			ev:   api.RunEvent{RunID: validID, MaxRepairRounds: intp(-1)},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "max-repairs omitted when over the ceiling",
			ev:   api.RunEvent{RunID: validID, MaxRepairRounds: intp(maxRepairRoundsCeiling + 1)},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "merge-policy pr appended",
			ev:   api.RunEvent{RunID: validID, MergePolicy: "pr"},
			want: []string{"crank", "--run", validID, "--resume", "--merge-policy", "pr"},
		},
		{
			name: "merge-policy auto appended",
			ev:   api.RunEvent{RunID: validID, MergePolicy: "auto"},
			want: []string{"crank", "--run", validID, "--resume", "--merge-policy", "auto"},
		},
		{
			name: "merge-policy manual omitted",
			ev:   api.RunEvent{RunID: validID, MergePolicy: "manual"},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "merge-policy unknown value omitted",
			ev:   api.RunEvent{RunID: validID, MergePolicy: "rebase"},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "merge-policy empty omitted",
			ev:   api.RunEvent{RunID: validID, MergePolicy: ""},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "model alias appended",
			ev:   api.RunEvent{RunID: validID, Model: "opus"},
			want: []string{"crank", "--run", validID, "--resume", "--model", "opus"},
		},
		{
			name: "model dated id appended",
			ev:   api.RunEvent{RunID: validID, Model: "claude-opus-4-8"},
			want: []string{"crank", "--run", validID, "--resume", "--model", "claude-opus-4-8"},
		},
		{
			name: "model empty omitted",
			ev:   api.RunEvent{RunID: validID, Model: ""},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "model with space (flag injection) omitted",
			ev:   api.RunEvent{RunID: validID, Model: "opus --dangerously"},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "model with leading dash (flag) omitted",
			ev:   api.RunEvent{RunID: validID, Model: "-rf"},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "model with slash (path) omitted",
			ev:   api.RunEvent{RunID: validID, Model: "../etc/passwd"},
			want: []string{"crank", "--run", validID, "--resume"},
		},
		{
			name: "all flags together",
			ev:   api.RunEvent{RunID: validID, MaxTokens: 42, MergePolicy: "auto", Model: "sonnet"},
			want: []string{"crank", "--run", validID, "--resume", "--max-tokens", "42", "--merge-policy", "auto", "--model", "sonnet"},
		},
		{
			name:    "invalid non-uuid run id returns error and no argv",
			ev:      api.RunEvent{RunID: "not-a-uuid"},
			wantErr: true,
		},
		{
			name:    "empty run id returns error and no argv",
			ev:      api.RunEvent{RunID: ""},
			wantErr: true,
		},
		{
			name:    "run id with injected flag rejected",
			ev:      api.RunEvent{RunID: validID + " --evil"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pass a fresh copy of base so append cannot mutate a shared backing array.
			got, err := augmentRunArgv(append([]string(nil), base...), tt.ev)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (argv=%v)", got)
				}
				if got != nil {
					t.Fatalf("expected nil argv on error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("argv mismatch\n got: %v\nwant: %v", got, tt.want)
			}
		})
	}
}

func intp(n int) *int { return &n }

// TestValidMaxRepairs pins the fail-closed validation table (#569): only 0..ceiling is honored, and
// every rejection falls back to crank's own default with ok=false so the caller omits the flag.
func TestValidMaxRepairs(t *testing.T) {
	tests := []struct {
		name   string
		in     *int
		want   int
		wantOK bool
	}{
		{name: "nil (NULL column) falls back to the default", in: nil, want: defaultMaxRepairRounds},
		{name: "zero is a real value: no repair rounds", in: intp(0), want: 0, wantOK: true},
		{name: "two", in: intp(2), want: 2, wantOK: true},
		{name: "the ceiling itself", in: intp(maxRepairRoundsCeiling), want: maxRepairRoundsCeiling, wantOK: true},
		{name: "negative rejected", in: intp(-1), want: defaultMaxRepairRounds},
		{name: "over the ceiling rejected", in: intp(maxRepairRoundsCeiling + 1), want: defaultMaxRepairRounds},
		{name: "absurd value rejected", in: intp(1000), want: defaultMaxRepairRounds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := validMaxRepairs(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("validMaxRepairs(%v) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
