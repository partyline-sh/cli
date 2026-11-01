package theme

import "strings"

// Signal is exactly seven cells; frames only express activity, never percent complete.
func Signal(state string, frame int, ascii, animate bool) string {
	mark := "  ·  "
	switch state {
	case "working":
		mark = "▂▄▆▄▂"
	case "waiting":
		mark = "  ◆  "
	case "done":
		mark = "  ✓  "
	case "failed":
		mark = "  !  "
	}
	if ascii {
		switch state {
		case "working":
			mark = ".:|:."
		case "waiting":
			mark = "  !  "
		case "done":
			mark = " OK  "
		case "failed":
			mark = " ERR "
		default:
			mark = "  .  "
		}
	}
	if state == "working" && animate {
		levels := []rune("▁▂▄▆█▆▄▂")
		if ascii {
			levels = []rune("._-=|=-_")
		}
		var b strings.Builder
		for i := 0; i < 5; i++ {
			b.WriteRune(levels[(max(frame, 0)+i*2)%len(levels)])
		}
		mark = b.String()
	}
	return "[" + mark + "]"
}
