package theme

import (
	"fmt"
	"strconv"
)

// Blend returns n colours spread evenly across the gradient's three stops, for drawing the
// gradient one cell at a time where a renderer has no gradient of its own (the tmux tab bar).
func (t Tokens) Blend(n int) []string {
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return []string{t.Gradient[0]}
	}
	out := make([]string, n)
	for i := range out {
		pos := float64(i) / float64(n-1) * 2 // 0..2 across the two segments
		seg := min(int(pos), 1)
		out[i] = mix(t.Gradient[seg], t.Gradient[seg+1], pos-float64(seg))
	}
	return out
}

// mix interpolates two #rrggbb colours; f=0 is a, f=1 is b.
func mix(a, b string, f float64) string {
	ca, cb := rgb(a), rgb(b)
	var out [3]int
	for i := range out {
		out[i] = int(float64(ca[i]) + (float64(cb[i])-float64(ca[i]))*f + 0.5)
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
}

// rgb parses #rrggbb; anything else is black rather than a panic, since Tokens is exported and a
// caller may hand Blend a palette with no gradient set.
func rgb(hex string) [3]int {
	var c [3]int
	if !hexColour.MatchString(hex) {
		return c
	}
	for i := range c {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err == nil {
			c[i] = int(v)
		}
	}
	return c
}
