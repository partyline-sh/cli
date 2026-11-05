package main

import (
	"fmt"
	"strings"
)

func printLoopReviews(reviews []loopReview) {
	for _, r := range reviews {
		fmt.Printf("\n%s", termSafe(r.Engine))
		if r.Tokens.In+r.Tokens.Out > 0 {
			fmt.Printf(" · %dk in, %dk cached, %dk out", r.Tokens.In/1000, r.Tokens.Cached/1000, r.Tokens.Out/1000)
		}
		if r.Error != "" {
			fmt.Printf("\n  failed: %s\n", termSafe(r.Error))
			continue
		}
		fmt.Printf(" · %s · %d findings, %d dropped\n", termSafe(r.Verdict), len(r.Findings), len(r.Dropped))
		for _, p := range []string{"P1", "P2", "P3"} {
			for _, f := range r.Findings {
				if f.Priority == p {
					fmt.Printf("  %s %s:%d  %s\n", p, termSafe(f.File), f.Line, termSafe(f.Claim))
					if f.Fix != "" {
						fmt.Printf("     fix: %s\n", termSafe(f.Fix))
					}
				}
			}
		}
		for _, c := range r.UnverifiedClaims {
			fmt.Printf("  unverified: %s\n", termSafe(c))
		}
		for _, f := range r.Dropped {
			fmt.Printf("  dropped %s:%d  %s\n", termSafe(f.File), f.Line, termSafe(f.Dropped))
		}
	}
}

// termSafe makes model-written text safe to print: one line, no control characters. Reviewer text
// can carry content injected through the repository, and an escape sequence in it could redraw
// the terminal or forge extra lines.
func termSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, s)
}

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}
