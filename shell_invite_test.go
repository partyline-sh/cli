package main

import (
	"reflect"
	"testing"
)

// `ptln start --invite @peetoose` used to print "invites sent: 1" and deliver nothing. The
// accounting below is the fix, and the cases that matter are the ones where a target is
// neither delivered nor unresolved — a server-side cap, a dedupe, a failed send. Those must
// never land on the sent side of the line just because nothing said they failed.
func TestInviteOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		targets    []string
		accepted   []string
		unresolved []string
		sent       []string
		missed     []string
		quiet      []string
	}{
		{
			name:       "mixed list: the email went out, the handle did not",
			targets:    []string{"real@person.com", "@ghost"},
			accepted:   []string{"real@person.com"},
			unresolved: []string{"@ghost"},
			sent:       []string{"real@person.com"},
			missed:     []string{"@ghost"},
		},
		{
			name:     "everything resolved and was delivered",
			targets:  []string{"a@b.com", "@peetoose"},
			accepted: []string{"a@b.com", "@peetoose"},
			sent:     []string{"a@b.com", "@peetoose"},
		},
		{
			name:       "nothing resolved",
			targets:    []string{"@ghost"},
			unresolved: []string{"@ghost"},
			missed:     []string{"@ghost"},
		},
		{
			// The regression the reviewer caught: the server resolved it fine but never
			// delivered it (send failed / trimmed by a cap). Silence is not success.
			name:     "resolved but never delivered is NOT reported as sent",
			targets:  []string{"a@b.com", "c@d.com"},
			accepted: []string{"a@b.com"},
			quiet:    []string{"c@d.com"},
			sent:     []string{"a@b.com"},
		},
		{
			name:    "a total delivery failure names every target",
			targets: []string{"a@b.com", "@peetoose"},
			quiet:   []string{"a@b.com", "@peetoose"},
		},
		{
			// Two spellings of one handle collapse server-side; only one can be accepted,
			// and the other must show as undelivered rather than riding along.
			name:     "a deduped duplicate is not silently counted",
			targets:  []string{"@peetoose", "peetoose"},
			accepted: []string{"@peetoose"},
			sent:     []string{"@peetoose"},
			quiet:    []string{"peetoose"},
		},
		{
			name:       "case and spacing differences still match",
			targets:    []string{"@Ghost"},
			unresolved: []string{" @ghost "},
			missed:     []string{"@Ghost"},
		},
		{
			name:     "a name the host never typed is not printed back at them",
			targets:  []string{"a@b.com"},
			accepted: []string{"a@b.com", "\x1b[2Jgotcha"},
			sent:     []string{"a@b.com"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, missed, quiet := inviteOutcome(tc.targets, tc.accepted, tc.unresolved)
			eq := func(label string, got, want []string) {
				if len(got) != len(want) || (len(got) > 0 && !reflect.DeepEqual(got, want)) {
					t.Errorf("%s = %q, want %q", label, got, want)
				}
			}
			eq("sent", sent, tc.sent)
			eq("missed", missed, tc.missed)
			eq("quiet", quiet, tc.quiet)
		})
	}
}

func TestJoined(t *testing.T) {
	if got := joined(" — ", nil); got != "" {
		t.Errorf("empty list should render nothing, got %q", got)
	}
	if got := joined(" — ", []string{"a", "b"}); got != " — a, b" {
		t.Errorf("got %q", got)
	}
}
