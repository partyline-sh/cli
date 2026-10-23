package ptymux

import "partyline.sh/partyline/internal/brand"

// barHint builds the right-hand end of the status bar: a mode pill plus the keys that are live
// in that mode. It replaces four hand-swapped hint strings that had drifted apart and never
// carried a mode badge at all, so LIVE and SPLIT looked identical while their keys were not.
//
// ONE ROW, ALWAYS. The child screens are sized rows-1 (see bodySize / SnapshotHistory's
// viewRows pad, and regression #238), so the bar occupying a second row would silently
// mis-size every child and put snapshot replay one line off. brand.HintBar never emits a
// newline; nothing here may either. Width budgeting stays in the caller, in visCols — display
// columns, the same metric clipANSI cuts on.
//
// The hints are DERIVED from the same state the dispatcher branches on: `split` is the
// splitActive() guard that gates tab/z/x at handleKey, and setup/pfx/selecting are the exact
// flags the input loop reads. Same rule drawCmdPanel follows — never a parallel list.
func barHint(selecting, pfx, setup, split bool, width int) string {
	switch {
	case selecting:
		return brand.HintBar("SELECT", []brand.Hint{
			{Key: "←/→", Label: "move"}, {Key: "1-9", Label: "jump"},
			{Key: "⏎", Label: "switch"}, {Key: "esc", Label: "cancel"}}, width)
	case pfx:
		// The chord is armed — the command panel above lists every command, so the bar only
		// marks that we're waiting for a key. (drawCmdPanel owns command discoverability.)
		return brand.HintBar("CHORD", []brand.Hint{
			{Label: "pick a command"}, {Key: "esc", Label: "cancels"}}, width)
	case setup:
		// The guided split's one instruction line — it already names esc. See split.go.
		return brand.HintBar("SETUP", []brand.Hint{{Label: splitSetupHint}}, width)
	case split:
		// tab / z / x mean something different here than in a full-width session, and nothing
		// on screen said so before the pill did.
		return brand.HintBar("SPLIT", []brand.Hint{
			{Key: "tab", Label: "focus"}, {Key: "z", Label: "zoom"},
			{Key: "x", Label: "close pane"}, {Key: "^\\", Label: "menu"}}, width)
	default:
		// LIVE stays a minimal nudge on purpose: every key belongs to the CHILD here, and the
		// mux's own command list lives one keystroke away in the ctrl-\ panel.
		return brand.HintBar("LIVE", []brand.Hint{{Key: "^\\", Label: "menu"}}, width)
	}
}
