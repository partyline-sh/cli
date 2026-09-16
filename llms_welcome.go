// The welcome screen — partyline's front door when the switchboard has nothing to show
// (bare `ptln` with zero sessions) and on demand via `ptln welcome`. A centered brand
// wordmark over a short list of "doors", each wired to an EXISTING entry point: resume
// the newest session, a fresh mux terminal (the switchboard's 'n'), a shared shell
// (`ptln start`), the Planning agent (`ptln plan`), and the switchboard's search.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// welcomeWantSearch asks runLLMSApp to open the switchboard with search already active
// (the welcome screen's "/ find a session" door). Consumed once at launcher build.
var welcomeWantSearch bool

type welcomeDoor struct {
	disp     string // the key as shown ("↵", "n", …)
	key      byte   // the raw byte that activates it directly
	title    string
	subtitle string
	run      func() error // dispatched after the terminal is restored
}

// welcomeMain is `ptln welcome` — always shows the screen, sessions or not.
func welcomeMain(_ []string) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println("ptln welcome needs an interactive terminal")
		return
	}
	if err := runWelcome(); err != nil {
		fatal(err)
	}
}

// lastResumable is the newest resumable user session (the same data + "last used" rule
// the switchboard sorts by), with when it was last touched. nil when there are none.
func lastResumable(all []aiSession, meta map[string]sessMeta) (*aiSession, time.Time) {
	var best *aiSession
	var bestT time.Time
	for i := range all {
		s := &all[i]
		if s.resumeArgv == nil || isAgentSession(*s) || meta[s.ID].Archived {
			continue
		}
		t := s.LastActive
		if lu := meta[s.ID].LastUsed; lu.After(t) {
			t = lu
		}
		if best == nil || t.After(bestT) {
			best, bestT = s, t
		}
	}
	return best, bestT
}

// runWelcome builds the doors from current session state, runs the full-screen picker,
// and dispatches the chosen door. q/esc opens the switchboard when there are sessions
// to switch, else just quits (the switchboard refuses to open empty).
func runWelcome() error {
	all := collectSessions()
	// The one moment the user is guaranteed to care: they came looking for a session and there
	// isn't one. Ask WHERE TO LOOK here rather than shipping a config key nobody would ever find —
	// a session started under a different HOME is invisible, not missing (see llms_roots.go).
	// Offered once per empty launch; declining just proceeds to the normal doors.
	if len(all) == 0 {
		if offerSessionRoots() {
			all = collectSessions()
		}
	}
	meta := loadLLMMeta()
	var doors []welcomeDoor
	if s, when := lastResumable(all, meta); s != nil {
		cp := *s
		doors = append(doors, welcomeDoor{"↵", '\r', "resume last session",
			cp.Tool + " · " + projLabel(cp.Cwd) + " · " + humanAge(when), func() error {
				mt := meta[cp.ID]
				mt.LastUsed = time.Now()
				meta[cp.ID] = mt
				saveLLMMeta(meta)
				spec := inheritRepoBindSpec(ptymux.Spec{Label: muxLabelFor(cp, meta), Key: cp.ID,
					Model: sessionModel(cp), Argv: cp.resumeArgv, Dir: cp.resumeDir})
				return runLLMSApp([]ptymux.Spec{spec})
			}})
	}
	doors = append(doors,
		welcomeDoor{"n", 'n', "new session", "", func() error {
			return runLLMSApp([]ptymux.Spec{*shellSpec()}) // the switchboard's 'n': a fresh mux terminal
		}},
		welcomeDoor{"s", 's', "share this shell", "", func() error {
			os.Args = os.Args[:1] // `ptln start` — shellMain's flag parser must see no verb
			shellMain()
			return nil
		}},
		welcomeDoor{"p", 'p', "plan something", "", func() error {
			describeMain(nil) // `ptln plan` — the Planning agent
			return nil
		}},
	)
	if len(all) > 0 { // searching an empty switchboard would just bounce back here
		doors = append(doors, welcomeDoor{"/", '/', "find a session", "", func() error {
			welcomeWantSearch = true
			return runLLMSApp(nil)
		}})
	}
	idx, err := welcomeLoop(doors, len(all) > 0)
	if err != nil || idx < 0 {
		return err
	}
	if idx == len(doors) { // q/esc with sessions present → the switchboard
		return runLLMSApp(nil)
	}
	return doors[idx].run()
}

// welcomeLoop owns the terminal (raw mode + alt screen) until a door is chosen. Returns
// the door index, len(doors) for "go to the switchboard", or -1 to quit.
func welcomeLoop(doors []welcomeDoor, canSwitch bool) (int, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return -1, err
	}
	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l")
	defer func() {
		os.Stdout.WriteString("\x1b[?25h\x1b[?1049l")
		_ = term.Restore(fd, old)
	}()
	sel := 0
	buf := make([]byte, 64)
	for {
		welcomeRender(doors, sel, canSwitch)
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return -1, nil
		}
		b := buf[:n]
		if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' { // arrows
			switch b[2] {
			case 'A':
				sel = (sel - 1 + len(doors)) % len(doors)
			case 'B':
				sel = (sel + 1) % len(doors)
			}
			continue
		}
		switch c := b[0]; c {
		case 0x1b, 'q', 0x03: // esc / q / ctrl-c
			if canSwitch {
				return len(doors), nil
			}
			return -1, nil
		case '\r', '\n':
			// Enter is also the resume door's shortcut — when it exists it's first and
			// default-selected, so plain ⏎ resumes; otherwise it opens the selection.
			return sel, nil
		case 'k':
			sel = (sel - 1 + len(doors)) % len(doors)
		case 'j':
			sel = (sel + 1) % len(doors)
		default:
			for i, d := range doors {
				if d.key == c {
					return i, nil
				}
			}
		}
	}
}

// welcomeRender paints one frame: wordmark, tagline, doors, bottom hint — all centered.
func welcomeRender(doors []welcomeDoor, sel int, canSwitch bool) {
	w, h := 80, 24
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 0 && r > 0 {
		w, h = c, r
	}
	center := func(s string) string {
		p := (w - brand.VisWidth(s)) / 2
		if p < 0 {
			p = 0
		}
		return strings.Repeat(" ", p) + s
	}
	lines := []string{
		center(brand.Wordmark()),
		"",
		center("\x1b[3;38;5;245mmultiplayer for your terminal\x1b[0m"),
		"", "",
	}
	// Doors align on the widest title so the column reads as a list, not ragged centers.
	dw := 0
	for _, d := range doors {
		if l := brand.VisWidth(d.title) + 3; l > dw {
			dw = l
		}
	}
	for i, d := range doors {
		key, title := "\x1b[38;5;245m"+d.disp+"\x1b[0m", "\x1b[38;5;250m"+d.title+"\x1b[0m"
		mark := "  "
		if i == sel {
			key = brand.Fg(brand.GradMid) + d.disp + "\x1b[0m"
			title = "\x1b[1;38;5;231m" + d.title + "\x1b[0m"
			mark = brand.Fg(brand.AmberRGB) + "▸\x1b[0m "
		}
		row := mark + key + "  " + title + strings.Repeat(" ", max(0, dw-brand.VisWidth(d.title)-3))
		if d.subtitle != "" {
			row += "  \x1b[38;5;240m" + d.subtitle + "\x1b[0m"
		}
		lines = append(lines, center(row))
	}
	// Derived from welcomeLoop's own dispatcher: it takes arrows and j/k, ⏎/the door's own
	// letter, and esc/q/ctrl-c — where q means "switchboard" only when there IS one to show.
	exit := brand.Hint{Key: "q · esc", Label: "quit"}
	if canSwitch {
		exit = brand.Hint{Key: "q · esc", Label: "switchboard"}
	}
	hint := brand.HintBar("WELCOME", []brand.Hint{
		{Key: "↑↓ · jk", Label: "choose"}, {Key: "↵", Label: "open"}, exit}, w)
	top := (h - len(lines) - 2) / 2
	if top < 1 {
		top = 1
	}
	var sb strings.Builder
	sb.WriteString("\x1b[2J\x1b[H")
	for i, l := range lines {
		fmt.Fprintf(&sb, "\x1b[%d;1H%s", top+i, brand.Clip(l, w))
	}
	fmt.Fprintf(&sb, "\x1b[%d;1H%s", h, brand.Clip(center(hint), w))
	os.Stdout.WriteString(sb.String())
}
