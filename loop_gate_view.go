package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	uitheme "partyline.sh/partyline/internal/theme"
)

// The approval popup's screens, in the Minimal direction: no boxes, a muted uppercase heading over
// a gradient rule, colour only for state and the primary key.

type gateStyle struct{ th uitheme.Theme }

func (s gateStyle) fg(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}
func (s gateStyle) text(v string) string  { return s.fg(s.th.Text).Render(v) }
func (s gateStyle) muted(v string) string { return s.fg(s.th.Muted).Render(v) }
func (s gateStyle) faint(v string) string { return s.fg(s.th.Faint).Render(v) }
func (s gateStyle) warn(v string) string  { return s.fg(s.th.Warning).Bold(true).Render(v) }
func (s gateStyle) key(k, label string) string {
	return s.fg(s.th.Focus).Bold(true).Render(k) + " " + s.muted(label)
}

// row is a label and a value with a hanging indent: a value that wraps stays in its column, so the
// labels down the left edge stay readable.
func (s gateStyle) row(label, value string, w int) string {
	const lw = 11
	return lipgloss.JoinHorizontal(lipgloss.Top,
		s.muted(fmt.Sprintf("%-*s", lw, label)),
		lipgloss.NewStyle().Width(max(w-lw, 20)).Render(value))
}

// section is a small muted heading over a group of rows.
func (s gateStyle) section(title string) string {
	return s.fg(s.th.Faint).Bold(true).Render(strings.ToUpper(title))
}

// code sets quoted source apart from prose.
func (s gateStyle) code(v string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(s.th.Text)).Background(lipgloss.Color(s.th.Raised)).Padding(0, 1).Render(v)
}

// heading is a muted uppercase title with the gradient as a rule under it.
func (s gateStyle) heading(title string, w int) string {
	n := min(lipgloss.Width(title)+8, w)
	var rule strings.Builder
	for _, c := range s.th.Blend(n) {
		rule.WriteString(s.fg(c).Render("─"))
	}
	return s.fg(s.th.Muted).Bold(true).Render(strings.ToUpper(title)) + "\n" + rule.String()
}

func (m gateModel) View() tea.View {
	st, lines, keys, room := m.frame()
	if len(lines) > room {
		start := min(m.scroll, len(lines)-room)
		lines = lines[start : start+room]
		keys += "   " + st.key("pgup/pgdn", "more")
	}
	width := max(m.w, 40)
	keyStyle := lipgloss.NewStyle().Width(width - 4)
	if r := m.current(); m.remoteQR != "" || (r != nil && r.Remote != nil && r.Remote.Pair && m.mode != gateDone) {
		keyStyle = keyStyle.Align(lipgloss.Center)
	}
	content := strings.Join(lines, "\n") + "\n\n" + keyStyle.Render(keys)
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(content))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// maxScroll is the furthest the body can scroll: Update clamps to it, so pgup moves at once after
// holding pgdn.
func (m gateModel) maxScroll() int {
	_, lines, _, room := m.frame()
	return max(len(lines)-room, 0)
}

// frame is the body wrapped to the popup as lines, the keys under it, and how many body lines fit.
// Width wraps long values inside the popup; the body scrolls when it is taller than the popup, and
// the keys stay on screen below it.
func (m gateModel) frame() (gateStyle, []string, string, int) {
	st := gateStyle{tmuxTheme()}
	var body, keys string
	switch {
	case m.remoteQR != "":
		left := max(int(time.Until(m.remoteUntil).Round(time.Second).Seconds()), 0)
		body = st.text("Pair a phone") + "\n\n" + m.remoteQR + "\n\n" +
			st.text("Scan with your phone's camera.") + "\n" +
			st.muted(fmt.Sprintf("New code in %ds", left))
		if m.result != "" {
			body += "\n\n" + st.warn(termSafe(m.result))
		}
		keys = st.key("esc", "close")
	case m.mode == gateDone:
		body, keys = m.doneScreen(st)
	case m.mode == gateDetails:
		body = st.heading("Full brief", m.w) + "\n" + m.vp.View()
		keys = st.key("esc", "back") + "   " + st.key("↑↓", "scroll")
	case m.list && m.mode == gateSummary && len(m.runs) > 0:
		body, keys = m.listScreen(st)
	case len(m.runs) == 0:
		body = st.heading("Approvals", m.w) + "\n\n" + st.muted("Nothing is waiting for you.")
		if m.result != "" {
			body += "\n\n" + st.text(m.result)
		}
		keys = st.key("esc", "close")
	default:
		body, keys = m.summary(st)
	}
	width := max(m.w, 40)
	if m.remoteAssets != "" {
		body = st.warn(termSafe(m.remoteAssets)) + "\n\n" + body
	}
	style := lipgloss.NewStyle().Width(width - 4)
	// The pairing screens are a small centred card: the QR code, then the code to compare.
	if r := m.current(); m.remoteQR != "" || (r != nil && r.Remote != nil && r.Remote.Pair && m.mode != gateDone) {
		style = style.Align(lipgloss.Center)
	}
	lines := strings.Split(style.Render(body), "\n")
	return st, lines, keys, max(m.h-4-lipgloss.Height(keys), 3)
}

// summary is what the person reads before deciding: what the run is, where, what approving does.
// Everything here comes from the session that wants approval, so all of it goes through termSafe.
func (m gateModel) summary(st gateStyle) (string, string) {
	r := m.current()
	if r.Remote != nil {
		p := r.Remote.Phone
		label := "Revoke phone "
		keys := st.key("a", "revoke")
		if r.Remote.Pair {
			label = "Pair phone "
			keys = st.key("a", "approve") + "   " + st.key("r", "refuse")
		}
		body := st.heading("Phones", m.w) + "\n\n" + st.text(termSafe(label+p.Name+", fingerprint "+p.Fingerprint))
		if r.Remote.Pair {
			code := phonePairCode(p.Static)
			body = st.text("Pair a phone") + "\n\n" + bigDigitsTyped(st, code, len(code), false) + "\n\n" +
				st.text("Does your phone show "+code+"?") + "\n" + st.muted("If it does, press ⏎ to pair. If not, press esc.")
			keys = st.key("⏎", "pair") + "   " + st.key("esc", "refuse")
			if m.result != "" {
				body += "\n\n" + st.text(m.result)
			}
			return body, keys
		}
		if !r.Remote.Pair {
			last := "never"
			if p.LastSeen != nil {
				last = p.LastSeen.Format(time.RFC3339)
			}
			body += "\nPaired " + p.PairedAt.Format(time.RFC3339) + "\nLast seen: " + last
		}
		if m.mode == gateConfirm {
			keys = m.confirmPrompt(st)
		}
		if m.result != "" {
			body += "\n\n" + st.text(m.result)
		}
		return body, keys + "   " + st.key("esc", "back")
	}
	in := m.info[r.ID]
	w := max(m.w-4, 40)
	if r.State == loopAwaitYou {
		return m.disputeSummary(st, r, in, w)
	}
	if !loopWaiting(r) {
		return m.runningSummary(st, r, w)
	}
	var b strings.Builder
	heading, approving := "Approve a loop", "runs the checks now as a baseline; then the session builds, and partyline checks and reviews each round"
	if batchPending(r) {
		heading, approving = "Approve a batch", "approves every step below; they run one after another without stopping, each checked and reviewed. Disputed findings wait for you; a failed step stops the batch"
	}
	fmt.Fprintf(&b, "%s\n\n", st.heading(heading, w))
	fmt.Fprintf(&b, "%s %s\n\n", st.warn(uitheme.NeedsYou.Glyph()+" loop "+termSafe(r.ID)), st.faint(fmt.Sprintf("%d of %d waiting", m.sel+1, len(m.runs))))
	checks := make([]string, len(r.Checks))
	for i, c := range r.Checks {
		checks[i] = st.code("$ " + termSafe(c))
	}
	// A batch's goal is a line per step: each is made safe on its own, so the lines stay lines.
	goal := strings.Split(in.goal, "\n")
	for i := range goal {
		goal[i] = st.text(termSafe(goal[i]))
	}
	fmt.Fprintf(&b, "%s\n\n", st.row("goal", strings.Join(goal, "\n"), w))
	fmt.Fprintf(&b, "%s\n", st.section("What runs"))
	fmt.Fprintf(&b, "%s\n", st.row("where", termSafe(repoLabel(r.Repo))+" · "+termSafe(in.tab), w))
	fmt.Fprintf(&b, "%s\n", st.row("brief", termSafe(in.revision), w))
	fmt.Fprintf(&b, "%s\n", st.row("checks", strings.Join(checks, "\n"), w))
	reviewers := termSafe(r.Review)
	if r.Config != nil {
		reviewers = configDescription(r.Config)
	}
	fmt.Fprintf(&b, "%s\n\n", st.row("reviewers", termSafeLines(reviewers), w))
	if batchPending(r) {
		// What the reviewers concluded, and what approving dismisses, are on this screen, not only
		// in the full brief.
		secs := mdSections(latestBrief(r))
		fmt.Fprintf(&b, "%s\n", st.section("Batch review"))
		result := termSafe(firstLine(secs["batch review"]))
		if strings.Contains(result, "open P1") {
			// Approving here accepts problems the reviewers found: it is not shown as plain text.
			fmt.Fprintf(&b, "%s\n", st.row("result", st.warn(result+" (v shows them)"), w))
		} else {
			fmt.Fprintf(&b, "%s\n", st.row("result", st.text(result), w))
		}
		if d := strings.TrimSpace(secs["disputed by the session"]); d != "" {
			n := strings.Count(d, "the session says:")
			fmt.Fprintf(&b, "%s\n", st.row("disputed", st.warn(fmt.Sprintf("%d finding(s) the session disputes; approving dismisses them (v shows them)", n)), w))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s", st.row("approving", st.muted(approving), w))
	// The note and the code sit in the fixed area under the summary, so they stay on screen when
	// the summary scrolls.
	if m.mode == gateNote {
		k := st.text("Reject with a note for the session:") + "\n" + m.note.View()
		if m.result != "" {
			k += "\n" + st.warn(m.result)
		}
		return b.String(), k + "\n" + st.key("⏎", "send") + "   " + st.key("esc", "cancel")
	}
	if m.mode == gateConfirm {
		return b.String(), m.confirmPrompt(st)
	}
	if m.result != "" {
		fmt.Fprintf(&b, "\n\n%s", st.text(m.result))
	}
	keys := st.key("a", "approve") + "   " + st.key("r", "reject with a note") + "   " + st.key("v", "full brief")
	if len(m.runs) > 1 {
		keys += "   " + st.key("↑↓", "other runs")
	}
	return b.String(), keys + "   " + st.key("s", "stop") + "   " + st.key("esc", "close")
}

func (m gateModel) disputeSummary(st gateStyle, r *loopRun, in gateInfo, w int) (string, string) {
	t := in.dispute
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", st.heading("Decide a disputed finding", w))
	fmt.Fprintf(&b, "%s  %s\n\n", st.warn(uitheme.NeedsYou.Glyph()+" "+termSafe(t.ID)+" on loop "+termSafe(r.ID)), st.faint(termSafe(repoLabel(r.Repo))+" · "+termSafe(in.tab)))
	fmt.Fprintf(&b, "%s\n", st.section("The finding"))
	fmt.Fprintf(&b, "%s\n", st.row("where", fmt.Sprintf("%s:%d", termSafe(t.File), t.Line), w))
	fmt.Fprintf(&b, "%s\n\n", st.row("code", st.code(strings.TrimSpace(termSafe(t.Quote))), w))
	fmt.Fprintf(&b, "%s\n", st.section(termSafe(t.Engine)+" says"))
	fmt.Fprintf(&b, "%s\n", st.row("problem", st.text(termSafe(t.Claim)), w))
	fmt.Fprintf(&b, "%s\n\n", st.row("fix", termSafe(t.Fix), w))
	fmt.Fprintf(&b, "%s\n", st.section("The session says"))
	fmt.Fprintf(&b, "%s", st.row("why not", st.text(termSafe(t.Dispute)), w))
	if m.result != "" {
		fmt.Fprintf(&b, "\n\n%s", st.text(m.result))
	}
	if m.mode == gateConfirm {
		return b.String(), m.confirmPrompt(st)
	}
	return b.String(), st.key("f", "fix it") + "   " + st.key("d", "dismiss the finding") + "   " + st.key("v", "full brief") + "   " + st.key("esc", "close")
}

// confirmPrompt asks for the code shown only here, drawn large so it is the first thing seen.
func (m gateModel) confirmPrompt(st gateStyle) string {
	typed := m.typed + strings.Repeat("·", len(m.code)-len(m.typed))
	prompt := st.warn("type this code to "+m.label) + "\n\n" + bigDigitsTyped(st, m.code, len(m.typed), m.blink) + "\n\n" +
		st.text("typed  "+typed) + "   " + st.key("esc", "cancel")
	if m.result != "" {
		prompt = st.text(m.result) + "\n" + prompt
	}
	return prompt
}

// bigDigitsTyped draws the code large with the person's progress on it: digits already typed in the
// success colour, the rest in the warning colour, and under the next one a cursor that blinks, so
// what to type next is as obvious as the code.
func bigDigitsTyped(st gateStyle, code string, typed int, blink bool) string {
	rows := make([]string, 6)
	n := 0
	for _, c := range code {
		if c < '0' || c > '9' {
			continue
		}
		colour := st.th.Warning
		if n < typed {
			colour = st.th.Success
		}
		cursor := "     "
		if n == typed && blink {
			cursor = "▀▀▀▀▀"
		}
		for i := range rows {
			if n > 0 {
				rows[i] += "   "
			}
			if i == 5 {
				rows[i] += st.fg(st.th.Focus).Bold(true).Render(cursor)
				continue
			}
			rows[i] += st.fg(colour).Bold(true).Render(bigDigitRows[c-'0'][i])
		}
		n++
	}
	return strings.Join(rows, "\n")
}

// doneScreen says what a decision did, large, and that the popup is about to close.
func (m gateModel) doneScreen(st gateStyle) (string, string) {
	word := strings.ToUpper(m.done[:1]) + m.done[1:]
	body := st.heading("Done", m.w) + "\n\n" + st.fg(st.th.Success).Bold(true).Render("✓ "+word)
	if m.doneOn != "" {
		body += st.text("  " + m.doneOn)
	}
	next := fmt.Sprintf("closes in %d seconds", int(gateDoneFor/time.Second))
	if len(m.runs) > 0 {
		next = fmt.Sprintf("%d more waiting: back to the list in %d seconds", len(m.runs), int(gateDoneFor/time.Second))
	}
	return body + "\n\n" + st.muted(next), st.key("esc", "now")
}

// bigDigitRows are 0-9 drawn five rows tall.
var bigDigitRows = [10][5]string{
	{"█████", "█   █", "█   █", "█   █", "█████"},
	{"  █  ", " ██  ", "  █  ", "  █  ", " ███ "},
	{"█████", "    █", "█████", "█    ", "█████"},
	{"█████", "    █", " ████", "    █", "█████"},
	{"█   █", "█   █", "█████", "    █", "    █"},
	{"█████", "█    ", "█████", "    █", "█████"},
	{"█████", "█    ", "█████", "█   █", "█████"},
	{"█████", "    █", "   █ ", "  █  ", "  █  "},
	{"█████", "█   █", "█████", "█   █", "█████"},
	{"█████", "█   █", "█████", "    █", "█████"},
}

// bigDigits is code drawn in bigDigitRows, three spaces between digits; other runes are skipped.
func bigDigits(code string) string {
	rows := make([]string, 5)
	for _, c := range code {
		if c < '0' || c > '9' {
			continue
		}
		for i := range rows {
			if rows[i] != "" {
				rows[i] += "   "
			}
			rows[i] += bigDigitRows[c-'0'][i]
		}
	}
	return strings.Join(rows, "\n")
}

// briefGoal is the first line of the brief's Goal section.
func briefGoal(brief string) string {
	for _, l := range strings.Split(mdSections(brief)["goal"], "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "(the brief has no goal)"
}

// briefRevisionNote says which revision this is, which sections changed from the previous one, and
// the person's note if they rejected that previous revision.
func briefRevisionNote(r *loopRun) string {
	if r.Revision <= 1 {
		return "first version"
	}
	prev, _ := os.ReadFile(loopFile(r.ID, fmt.Sprintf("brief-%d.md", r.Revision-1)))
	note := fmt.Sprintf("revision %d", r.Revision)
	if changed := changedSections(string(prev), latestBrief(r)); len(changed) > 0 {
		note += "; changed: " + strings.Join(changed, ", ")
	} else {
		note += "; no section changed"
	}
	if rej := rejectionOf(r.ID, r.Revision-1); rej != "" {
		note += fmt.Sprintf("; you rejected revision %d: %q", r.Revision-1, rej)
	}
	return note
}

// changedSections names the brief sections that differ between two revisions, in brief order.
func changedSections(prev, cur string) []string {
	a, b := mdSections(prev), mdSections(cur)
	var out []string
	for _, s := range loopBriefSections {
		k := strings.ToLower(s)
		if strings.TrimSpace(a[k]) != strings.TrimSpace(b[k]) {
			out = append(out, s)
		}
	}
	return out
}

// rejectionOf is the person's note on rejecting revision rev, if they did. Revisions are counted
// through the event log: the run starts at 1 and each brief_revised event moves to the next.
func rejectionOf(id string, rev int) string {
	evs, _ := loopEvents(id)
	cur, note := 1, ""
	for _, e := range evs {
		switch e.Type {
		case "brief_revised":
			cur++
		case "rejected":
			if cur == rev {
				note = e.Note
				if n, ok := e.Data["note"].(string); ok {
					note = n
				}
			}
		}
	}
	return note
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "(no review recorded)"
}

// runningSummary is an active run: nothing to decide, but it can be stopped.
func (m gateModel) runningSummary(st gateStyle, r *loopRun, w int) (string, string) {
	var b strings.Builder
	heading := "Running " + gateRunKind(r)
	if !r.live() {
		heading = "Finished batch" // listed only for its arbiter dismissals
	}
	fmt.Fprintf(&b, "%s\n\n", st.heading(heading, w))
	fmt.Fprintf(&b, "%s %s\n\n", st.text(uitheme.Working.Glyph()+" loop "+termSafe(r.ID)), st.faint(fmt.Sprintf("%d of %d", m.sel+1, len(m.runs))))
	fmt.Fprintf(&b, "%s\n", st.row("step", termSafe(briefTitle(latestBrief(r))), w))
	fmt.Fprintf(&b, "%s\n", st.row("state", termSafe(r.State)+" · round "+fmt.Sprint(r.Round), w))
	fmt.Fprintf(&b, "%s", st.row("now", termSafe(r.Note), w))
	if since := sinceLastEvent(r); since != "" {
		fmt.Fprintf(&b, "\n%s", st.row("for", since, w))
	}
	if p := reviewProgress(r); p != "" {
		fmt.Fprintf(&b, "\n%s", st.row("reviewers", p, w))
	}
	standing := gateStanding(r)
	if len(standing) > 0 {
		fmt.Fprintf(&b, "\n\n%s\n", st.section("Dismissed by the arbiter"))
		for i, a := range standing {
			id := fmt.Sprintf("S%d-%s", a.Step, a.Finding.ID)
			if i == 0 {
				id += " (x reverses)"
			}
			fmt.Fprintf(&b, "%s\n", st.row(fmt.Sprintf("%.2f", a.Confidence), st.text(termSafe(id+": "+a.Finding.Claim))+"\n"+st.muted("dispute: "+termSafe(a.Finding.Dispute))+"\n"+st.muted("arbiter: "+termSafe(a.Reason)), w))
		}
	}
	if m.mode == gateConfirm {
		return b.String(), m.confirmPrompt(st)
	}
	if m.result != "" {
		fmt.Fprintf(&b, "\n\n%s", st.text(m.result))
	}
	keys := st.key("s", "stop the "+gateRunKind(r))
	if !r.live() {
		keys = ""
	}
	if len(standing) > 0 {
		keys = strings.TrimSpace(st.key("x", "reverse a dismiss") + "   " + keys)
	}
	if len(m.runs) > 1 {
		keys += "   " + st.key("↑↓", "other runs")
	}
	return b.String(), keys + "   " + st.key("esc", "close")
}

// listScreen is the popup's first screen: every waiting item, then active runs, one row each.
// The keys act on the selected row as on its own screen; ⏎ opens it.
func (m gateModel) listScreen(st gateStyle) (string, string) {
	w := max(m.w-4, 40)
	var b strings.Builder
	waiting := 0
	for _, r := range m.runs {
		if loopWaiting(r) && (r.Remote == nil || r.Remote.Pair) {
			waiting++
		}
	}
	fmt.Fprintf(&b, "%s\n\n", st.heading(fmt.Sprintf("%d waiting for you", waiting), w))
	for i := range m.runs {
		b.WriteString(m.listRow(st, i, w) + "\n")
	}
	if m.result != "" {
		fmt.Fprintf(&b, "\n%s", st.text(m.result))
	}
	keys := st.key("⏎", "open") + "   " + st.key("↑↓", "move") + "   " + st.key("a", "approve") + "   " + st.key("s", "stop") + "   " + st.key("esc", "close")
	if r := m.current(); r != nil && r.Remote != nil {
		action := "revoke"
		if r.Remote.Pair {
			action = "approve"
		}
		keys = st.key("⏎", "open") + "   " + st.key("↑↓", "move") + "   " + st.key("a", action) + "   " + st.key("esc", "close")
	}
	if m.mode == gateConfirm {
		keys = m.confirmPrompt(st)
	}
	return b.String(), keys
}

// gateKind is what an item asks of the person.
func gateKind(r *loopRun) string {
	if r.Remote != nil {
		if r.Remote.Pair {
			return "pair phone"
		}
		return "paired phone"
	}
	switch {
	case !loopWaiting(r):
		return "running " + gateRunKind(r)
	case batchPending(r):
		return "batch"
	case r.State == loopAwaitYou && r.Batch != "" && deferredReviewRun(r):
		return "deferred findings"
	case r.State == loopAwaitYou:
		return "dispute"
	case r.Revision > 1:
		return "revised brief"
	}
	return "brief"
}

func gateTitle(r *loopRun) string {
	if r.Remote != nil {
		return r.Remote.Phone.Name + ", fingerprint " + r.Remote.Phone.Fingerprint
	}
	if batchPending(r) {
		if b, err := loadBatch(r.Batch); err == nil {
			return fmt.Sprintf("Batch of %d steps", len(b.Briefs))
		}
	}
	return briefTitle(latestBrief(r))
}

// gateRunKind names the target of a stop.
func gateRunKind(r *loopRun) string {
	if r.Batch != "" {
		return "batch"
	}
	return "loop"
}

// sinceLastEvent is how long the run has been in its state, e.g. "8m".
func sinceLastEvent(r *loopRun) string {
	evs, err := loopEvents(r.ID)
	if err != nil || len(evs) == 0 {
		return ""
	}
	return time.Since(evs[len(evs)-1].At).Round(time.Minute).String()
}

// reviewProgress names each reviewer of the current review and whether it has reported: a review
// file appears as each one finishes.
func reviewProgress(r *loopRun) string {
	var prefix string
	switch r.State {
	case loopBatchRev:
		prefix = fmt.Sprintf("review-b%d-", r.Revision)
	case loopChecking:
		prefix = fmt.Sprintf("review-r%d-", r.Round)
	default:
		return ""
	}
	engines, err := parseLoopEngines(r.Review)
	if err != nil {
		return ""
	}
	if r.Config != nil {
		engines = nil
		for _, slot := range r.Config.Reviewers {
			if len(slot) == 0 {
				continue
			}
			es, err := parseLoopEngines(slot[0])
			if err != nil {
				return ""
			}
			engines = append(engines, es[0])
		}
	}
	var parts []string
	for i, e := range engines {
		name := fmt.Sprintf("%s%d-%s.json", prefix, i+1, safeFileName(e.String()))
		label := e.String()
		mark := "reading"
		if _, err := os.Stat(loopFile(r.ID, name)); err == nil {
			mark = "done"
			if r.Config != nil {
				var rv loopReview
				readLoopJSON(r.ID, name, &rv)
				if rv.Engine != "" {
					label = rv.Engine
				}
				if !rv.LimitedUntil.IsZero() {
					mark = "limited"
				}
			}
		}
		parts = append(parts, termSafe(label)+" "+mark)
	}
	return strings.Join(parts, " · ")
}
