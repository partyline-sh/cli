// Package traypeer holds the tray's peer-messaging DECISIONS, with no reference to systray or Cocoa.
//
// WHY THIS PACKAGE EXISTS. cmd/ptln-tray is behind `//go:build darwin && tray` because it needs cgo,
// so `go test ./...` never compiles it — which meant the peer menu, a user-facing surface with a
// security invariant in it (notify on a rise only, never render question text into a banner), shipped
// with no test able to run at all. The systray calls cannot be tested without a GUI. Everything that
// DECIDES anything can, so it lives here: which rows are visible, what each row says, and which
// notification bodies a poll earns.
//
// The tray keeps its idiom: rows are PRE-ALLOCATED (systray can't grow a menu after start) and are
// shown, retitled, or hidden per poll. Section drives them through the Item interface, which
// *systray.MenuItem satisfies as-is and a test fake satisfies in three lines.
//
// THE TRAY STILL HOLDS NOTHING. Nothing here opens a socket, reads a token, or makes an HTTP call —
// it maps a snapshot the CLI already printed onto menu rows.
package traypeer

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	MaxRows = 4 // pre-allocated rows; must be ≥ the CLI's maxStateConsults, extras collapse into More
	QLines  = 3 // submenu lines the question is wrapped across
	QWidth  = 44
	Indent  = "   "
)

// Item is the slice of *systray.MenuItem this package needs. Nothing here enables, disables, or
// re-tooltips a row — those are set once at construction, in the tray.
type Item interface {
	SetTitle(string)
	Show()
	Hide()
}

// Consult is one queued question from a teammate's agent, as `ptln state` reports it.
type Consult struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Question   string `json:"question"`
	WaitingSec int    `json:"waiting_sec"`
}

// Snapshot mirrors the "peers" object in `ptln state`. A nil *Snapshot means the CLI had nothing to
// report — which is ALSO what a `ptln` predating the feature produces, since the key is omitted when
// every count is zero. Both are the same thing to this package: hide the section.
type Snapshot struct {
	Inbound      int       `json:"inbound"`
	Answered     int       `json:"answered"`
	AutoAnswered int       `json:"auto_answered"`
	AutoProject  string    `json:"auto_project"`
	Consults     []Consult `json:"consults"`
}

// Blocked is the contribution to the menu-bar badge: questions waiting on YOU, and nothing else.
// Auto-answered consults and landed replies are FYIs — padding the badge with those is how it becomes
// wallpaper. Nil-safe so the caller needn't branch on an old CLI.
func (s *Snapshot) Blocked() int {
	if s == nil {
		return 0
	}
	return s.Inbound
}

// Row is one pre-allocated question row: a parent item plus the submenu lines the question wraps
// across. It also remembers WHICH consult it currently stands for, because the queue moves under it
// and a click must act on the id the row shows NOW, not the one it showed when it was built.
type Row struct {
	Item   Item
	QLines []Item

	mu sync.Mutex
	id string
}

// CurrentID is what this row stands for right now; "" when the row is hidden. Read it at CLICK time.
func (r *Row) CurrentID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

func (r *Row) setID(id string) {
	r.mu.Lock()
	r.id = id
	r.mu.Unlock()
}

// Config is the wiring a Section needs. Post/Quiet are injected rather than called directly so the
// notification rules are testable without a notification centre.
type Config struct {
	Hdr, More, Auto, Repl Item
	Rows                  []*Row

	// Post delivers one notification body. nil drops them.
	Post func(string)
	// Quiet reports the tray-quiet switch. nil means never quiet.
	Quiet func() bool
}

// Section is the whole peer block plus the edge state its notifications need.
type Section struct {
	cfg Config

	// Last-seen counts for edge triggering. -1 means "first pass", so starting the tray never
	// announces what was already true before you started it.
	lastInbound, lastAuto, lastAnswered int
}

func NewSection(cfg Config) *Section {
	return &Section{cfg: cfg, lastInbound: -1, lastAuto: -1, lastAnswered: -1}
}

// Poll applies one `ptln state` read. ok=false means the read FAILED (no CLI, or one too old to print
// state at all) — which blanks the section WITHOUT touching the notification edges, because recording
// zero for a read that never happened would re-announce every waiting question the moment the CLI came
// back.
func (s *Section) Poll(ok bool, p *Snapshot) {
	if !ok {
		s.Hide()
		return
	}
	s.update(p)
}

// update repaints the block and fires whatever notification the change earned.
func (s *Section) update(p *Snapshot) {
	if p == nil {
		s.Hide()
		s.lastInbound, s.lastAuto, s.lastAnswered = 0, 0, 0
		return
	}

	if p.Inbound > 0 {
		s.cfg.Hdr.SetTitle(Pluralize(p.Inbound, "question needs you", "questions need you"))
		s.cfg.Hdr.Show()
	} else {
		s.cfg.Hdr.Hide()
	}

	for i, r := range s.cfg.Rows {
		if i >= len(p.Consults) {
			r.setID("")
			r.Item.Hide()
			continue
		}
		c := p.Consults[i]
		r.setID(c.ID)
		r.Item.SetTitle(fmt.Sprintf("◆ %s · waiting %s", c.Project, ShortWait(c.WaitingSec)))
		for j, line := range Wrap(c.Question, QWidth, len(r.QLines)) {
			r.QLines[j].SetTitle(line)
		}
		r.Item.Show()
	}

	if extra := p.Inbound - len(p.Consults); extra > 0 {
		s.cfg.More.SetTitle(fmt.Sprintf("%s…and %d more waiting", Indent, extra))
		s.cfg.More.Show()
	} else {
		s.cfg.More.Hide()
	}

	if p.AutoAnswered > 0 {
		label := Pluralize(p.AutoAnswered, "peer question answered today", "peer questions answered today")
		if p.AutoProject != "" {
			label += " · latest: " + p.AutoProject
		}
		s.cfg.Auto.SetTitle("✓ " + label)
		s.cfg.Auto.Show()
	} else {
		s.cfg.Auto.Hide()
	}

	if p.Answered > 0 {
		s.cfg.Repl.SetTitle("☎ " + Pluralize(p.Answered, "reply waiting", "replies waiting") + " — ctrl-\\ p to read")
		s.cfg.Repl.Show()
	} else {
		s.cfg.Repl.Hide()
	}

	s.notifyEdges(p)
}

// notifyEdges fires on a RISE only, never on a steady state — a banner every 4s is malware, not a
// feature. A repeated identical poll must therefore be silent.
//
// BODIES CARRY NAMES AND COUNTS, NEVER QUESTION OR ANSWER TEXT. A notification lands on a lock screen
// and in the system log, and a teammate's question is their words about their code — the one thing
// that must not end up there. A project label is a name; that's the line.
func (s *Section) notifyEdges(p *Snapshot) {
	if s.lastInbound >= 0 && p.Inbound > s.lastInbound {
		if p.Inbound == 1 && len(p.Consults) == 1 {
			s.post("A teammate's agent is waiting on you — about " + p.Consults[0].Project)
		} else {
			s.post(Pluralize(p.Inbound, "peer question is waiting on you", "peer questions are waiting on you"))
		}
	}
	// Auto-answered is pure FYI: your machine did something on a teammate's behalf and you didn't have
	// to be there. Worth telling you once, because the whole design is that it's otherwise invisible.
	if s.lastAuto >= 0 && p.AutoAnswered > s.lastAuto {
		if p.AutoProject != "" {
			s.post("Answered a teammate's read-only question about " + p.AutoProject)
		} else {
			s.post("Answered a teammate's read-only question")
		}
	}
	if s.lastAnswered >= 0 && p.Answered > s.lastAnswered {
		s.post("A peer answered your question — " + Pluralize(p.Answered, "1 reply waiting", "replies waiting"))
	}
	s.lastInbound, s.lastAuto, s.lastAnswered = p.Inbound, p.AutoAnswered, p.Answered
}

// post honours tray-quiet. The BADGE is the always-on signal; the banner is the interrupting one, so
// it's the one with a switch.
func (s *Section) post(body string) {
	if s.cfg.Post == nil {
		return
	}
	if s.cfg.Quiet != nil && s.cfg.Quiet() {
		return
	}
	s.cfg.Post(body)
}

// Hide blanks the section WITHOUT touching the notification edges — for when the read itself failed
// and we genuinely don't know what's waiting.
func (s *Section) Hide() {
	s.cfg.Hdr.Hide()
	s.cfg.More.Hide()
	s.cfg.Auto.Hide()
	s.cfg.Repl.Hide()
	for _, r := range s.cfg.Rows {
		r.setID("")
		r.Item.Hide()
	}
}

// Wrap word-wraps a question into EXACTLY n menu lines, ellipsising anything that doesn't fit.
// Always returns n lines so a shorter question CLEARS the leftovers from a longer one — a stale line
// under a new question would be the worst possible bug in a surface whose job is showing what you're
// about to approve.
//
// Width is counted in RUNES, not bytes. Menu width is visual, and a question written in Japanese or
// carrying an em-dash would otherwise wrap at a third of the intended length — and any truncation
// that cut bytes would split a rune and paint a replacement character into the one place the text has
// to be trustworthy.
func Wrap(text string, width, n int) []string {
	out := make([]string, n)
	if n <= 0 || width <= 0 {
		return out
	}

	var lines []string
	line := ""
	for _, w := range hardBreak(strings.Fields(text), width) {
		switch {
		case line == "":
			line = w
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		out[0] = Indent + "(empty question)"
		return out
	}
	if len(lines) > n {
		lines = lines[:n]
		lines[n-1] = ellipsize(lines[n-1], width) // mark it, rather than dropping the remainder silently
	}
	for i, l := range lines {
		out[i] = Indent + l
	}
	return out
}

// hardBreak splits any single word longer than the line width, at rune boundaries. Without this a
// 200-character URL or an unspaced CJK sentence overflows the menu on one line, and the wrap looks
// like it silently did nothing.
func hardBreak(words []string, width int) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		r := []rune(w)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		if len(r) > 0 {
			out = append(out, string(r))
		}
	}
	return out
}

// ellipsize marks a line as truncated, keeping it within width by dropping trailing RUNES.
func ellipsize(line string, width int) string {
	if utf8.RuneCountInString(line)+2 <= width {
		return line + " …"
	}
	r := []rune(line)
	for len(r)+1 > width && len(r) > 0 {
		r = r[:len(r)-1]
	}
	return strings.TrimRight(string(r), " ") + "…"
}

func ShortWait(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm", sec/60)
}

// Pluralize renders "1 <one>" / "N <many>". The count leads because that's what the sentence is about.
func Pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
