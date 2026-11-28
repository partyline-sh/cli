package theme

import "fmt"

// Brand palette: amber phone; wordmark gradient amber → pink → magenta.
var (
	AmberRGB      = [3]int{0xff, 0x98, 0x38}
	BrandGradFrom = [3]int{0xff, 0xaa, 0x3c}
	BrandGradMid  = [3]int{0xff, 0x5e, 0x8a}
	BrandGradTo   = [3]int{0xdf, 0x5b, 0xff}
)

// stops256 are the 256-colour stops nearest the gradient (amber, pinks, magentas), with
// approximate RGB for nearest-colour matching when truecolor isn't available.
var stops256 = []struct {
	idx int
	rgb [3]int
}{
	{214, [3]int{255, 175, 0}},   // amber / orange
	{211, [3]int{255, 135, 175}}, // light pink
	{205, [3]int{255, 95, 175}},  // pink
	{177, [3]int{215, 135, 255}}, // light magenta
	{171, [3]int{215, 95, 255}},  // magenta
}

func NearestBrand256(c [3]int) int {
	best, bestD := stops256[0].idx, 1<<62
	for _, s := range stops256 {
		dr, dg, db := c[0]-s.rgb[0], c[1]-s.rgb[1], c[2]-s.rgb[2]
		if d := dr*dr + dg*dg + db*db; d < bestD {
			best, bestD = s.idx, d
		}
	}
	return best
}

var PillRGB = [3]int{0xff, 0x5e, 0x8a}

// HexRGB renders a colour for consumers such as tmux that accept hex strings.
func HexRGB(rgb [3]int) string { return fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2]) }
