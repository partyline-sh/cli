package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The chat room as a Bubble Tea program: in a popup from ctrl-\ c, or pinned as a tab. The editor,
// history, completion and sending are the room's own (chatInput, chatSubmit); Bubble Tea's key
// events are translated into the room's keys so every key keeps its action. Waking members is the
// watcher's job, not the view's.

type chatPopup struct {
	sources     map[string]chatMessage // retain source so resizing reflows Markdown
	selection   chatSelection
	copyText    func(string) tea.Cmd
	copiedUntil time.Time
	copied      string // what the last copy did, shown until copiedUntil
	in          *chatInput
	lines       []string  // the room as shown: messages and notes, rendered
	seen        int       // last message seq shown
	scroll      int       // history lines scrolled back from the newest; pgup/pgdn
	oldest      int       // seq of the oldest message loaded; older ones load as the history scrolls up
	atStart     bool      // the room's first message is loaded
	newBelow    int       // messages that arrived while scrolled back
	oldestAt    time.Time // when the oldest loaded message was sent, shown while scrolled back
	opened      time.Time
	shown       map[string]bool // watcher notes already shown
	w, h        int
	pinned      bool         // running as a tab rather than a popup
	pin         func() error // opens the room as a tab
	saveTail    func(string) // keeps an unsent draft
}

type chatTick struct{}

// chatEdited is what $EDITOR left in the file.
type chatEdited struct {
	text string
	err  error
}

func chatTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return chatTick{} })
}

func newChatPopup(pinned bool) chatPopup {
	msgs := readChat()
	in := newChatInput(chatHistory(msgs))
	in.complete = chatComplete
	if b, err := os.ReadFile(chatDraftPath()); err == nil {
		in.ed.Set(string(b))
		in.ed.undo = nil
	}
	m := chatPopup{in: in, w: 100, h: 30, pinned: pinned, opened: time.Now(), shown: map[string]bool{},
		pin: chatPinAsTab, saveTail: chatSaveDraft}
	m = m.showNew(msgs, 40)
	return m
}

func (m chatPopup) Init() tea.Cmd { return chatTickCmd() }

// showNew adds messages after seen, keeping at most keep of a first load.
func (m chatPopup) showNew(msgs []chatMessage, keep int) chatPopup {
	var fresh []chatMessage
	for _, msg := range msgs {
		if msg.Seq > m.seen {
			fresh = append(fresh, msg)
		}
	}
	if keep > 0 && len(fresh) > keep {
		fresh = fresh[len(fresh)-keep:]
	}
	for _, msg := range fresh {
		entry := m.message(msg)
		m = m.appendHistory("", entry)
		m.seen = msg.Seq
		if m.oldest == 0 || m.oldestAt.IsZero() {
			m.oldest, m.oldestAt = msg.Seq, msg.At
		}
	}
	if keep > 0 && len(fresh) == len(msgs) {
		m.atStart = true
	}
	if m.scroll > 0 {
		m.newBelow += len(fresh)
	}
	return m
}

// cleared empties the screen; scrolling up brings the cleared messages back.
func (m chatPopup) cleared() chatPopup {
	m.selection = chatSelection{}
	m.lines, m.scroll, m.newBelow = nil, 0, 0
	m.sources = nil
	m.oldest, m.oldestAt, m.atStart = m.seen+1, time.Time{}, false
	return m
}

// chatOlderPage is how many older messages one scroll past the top loads.
const chatOlderPage = 100

// loadOlder puts the page of messages before the oldest loaded in front of the history. The scroll
// counts from the newest, so the lines on screen stay where they are.
func (m chatPopup) loadOlder(msgs []chatMessage) chatPopup {
	var older []chatMessage
	for _, msg := range msgs {
		if msg.Seq < m.oldest {
			older = append(older, msg)
		}
	}
	if len(older) <= chatOlderPage {
		m.atStart = true
	} else {
		older = older[len(older)-chatOlderPage:]
	}
	var lines []string
	for _, msg := range older {
		lines = append(lines, "", m.message(msg))
	}
	if len(older) > 0 {
		m.oldest, m.oldestAt = older[0].Seq, older[0].At
	}
	m.lines = append(lines, m.lines...)
	return m
}

// scrollBy moves the history n lines (positive is older), loading older messages at the top, and
// stops at the room's first line and at the newest.
func (m chatPopup) scrollBy(n int) chatPopup {
	st := gateStyle{tmuxTheme()}
	all, room, _, _, _, _ := m.layout(st)
	m.scroll = m.clampScroll(m.scroll, len(all), room) + n
	if n > 0 && m.scroll > len(all)-room && !m.atStart {
		m = m.loadOlder(readChat())
		all, room, _, _, _, _ = m.layout(st)
	}
	m.scroll = m.clampScroll(m.scroll, len(all), room)
	if m.scroll == 0 {
		m.newBelow = 0
	}
	return m
}

// keepPlace runs add, then moves the scroll so the top line on screen stays the top line: new
// history below and status lines coming or going (which change the history's height) do not
// move what a reader scrolled back is reading.
func (m chatPopup) keepPlace(add func(chatPopup) chatPopup) chatPopup {
	if m.scroll == 0 {
		return add(m)
	}
	st := gateStyle{tmuxTheme()}
	before, roomBefore, _, _, _, _ := m.layout(st)
	m.scroll = m.clampScroll(m.scroll, len(before), roomBefore)
	m = add(m)
	after, roomAfter, _, _, _, _ := m.layout(st)
	m.scroll = m.clampScroll(m.scroll+len(after)-len(before)-(roomAfter-roomBefore), len(after), roomAfter)
	return m
}

func (m chatPopup) width() int  { return max(1, min(92, m.w-4)) }
func (m chatPopup) margin() int { return max(0, (m.w-m.width())/2) }

// message registers a rendered entry; layout renders its source at the current width.
func (m *chatPopup) message(msg chatMessage) string {
	if m.sources == nil {
		m.sources = make(map[string]chatMessage)
	}
	key := chatFormat(msg, m.width())
	m.sources[key] = msg
	return key
}

func (m chatPopup) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			return m.scrollBy(3), nil
		}
		if msg.Button == tea.MouseWheelDown {
			return m.scrollBy(-3), nil
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if p, ok := m.historyCell(msg.X, msg.Y); ok {
				m.selection = chatSelection{anchor: p, end: p, dragging: true}
			}
		}
		return m, nil
	case tea.MouseMotionMsg:
		return m.dragTo(msg.X, msg.Y), nil
	case tea.MouseReleaseMsg:
		if msg.Button != tea.MouseLeft || !m.selection.dragging {
			return m, nil
		}
		if !m.selection.moved {
			m.selection.dragging = false
			return m, nil
		}
		m = m.dragTo(msg.X, msg.Y)
		m.selection.dragging = false
		text, _ := m.selectionText()
		copyText := m.copyText
		if copyText == nil {
			copyText = chatCopyText
		}
		return m, copyText(text)
	case chatCopied:
		switch {
		case msg.native:
			m.copied = fmt.Sprintf("copied %d lines", msg.lines)
		case msg.err != nil:
			m.copied = fmt.Sprintf("copy failed (%v); sent %d lines to the terminal clipboard", msg.err, msg.lines)
		default:
			m.copied = fmt.Sprintf("sent %d lines to the terminal clipboard", msg.lines)
		}
		m.copiedUntil = time.Now().Add(3 * time.Second)
		return m, nil
	case tea.WindowSizeMsg:
		m.selection = chatSelection{}
		m.w, m.h = msg.Width, msg.Height
		// A window that grew may now hold the whole history: no scroll is left to keep.
		return m.scrollBy(0), nil
	case chatTick:
		st := loadWatcherState()
		room := loadChatRoom()
		m = m.keepPlace(func(m chatPopup) chatPopup {
			m.in.status, _ = chatStatusLines(&room, readChat(), st.Wakes, st.Activity, time.Now())
			m = m.showNew(readChat(), 0)
			for _, n := range st.Notes {
				// By note, not by time: several members can go free in one watcher tick.
				key := n.At.String() + n.Text
				if n.At.After(m.opened) && !m.shown[key] {
					m = m.appendHistory(sgrDim + n.Text + sgrReset)
					m.shown[key] = true
				}
			}
			return m
		})
		return m, chatTickCmd()
	case chatEdited:
		if msg.err != nil {
			m.in.hint = "editor: " + msg.err.Error()
		} else {
			m.in.ed.Set(strings.TrimRight(msg.text, "\n"))
			m.saveTail(m.in.ed.String())
		}
		return m, nil
	case tea.PasteMsg:
		m.in.ed.Insert(cleanText(msg.Content)...) // one edit, as a bracketed paste always was
		m.saveTail(m.in.ed.String())
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "pgup": // the room's history scrolls; the input stays
			return m.scrollBy(max(m.h/2, 1)), nil
		case "pgdown":
			return m.scrollBy(-max(m.h/2, 1)), nil
		case "esc":
			if m.scroll > 0 { // back to the newest first; at the newest esc edits the input as before
				m.scroll, m.newBelow = 0, 0
				return m, nil
			}
		}
		if msg.String() == "ctrl+t" {
			// Pin: open the room as a tab and close the popup. In the tab, ctrl-t unpins: the
			// tab's program ends and tmux closes its window.
			if !m.pinned && m.pin != nil {
				_ = m.pin()
			}
			m.saveTail(m.in.ed.String())
			return m, tea.Quit
		}
		var res chatInputResult
		for _, k := range chatKeysFor(msg) {
			m.in.key(k, &res)
		}
		if !res.quit {
			// The draft is kept as it changes, so a popup closed by a detach, another popup or
			// a kill does not lose it.
			m.saveTail(m.in.ed.String())
		}
		return m.apply(res)
	}
	return m, nil
}

// apply does what the keys asked: send, clear, open the editor, or leave.
func (m chatPopup) apply(res chatInputResult) (tea.Model, tea.Cmd) {
	for _, text := range res.send {
		chatSubmit(text, func(s string) { m = m.appendHistory(s) }, func() { m = m.cleared() })
		m.scroll, m.newBelow = 0, 0  // sending returns to the newest
		m = m.showNew(readChat(), 0) // your own message appears at once
	}
	if res.clear {
		m = m.cleared()
	}
	if res.editor {
		cmd, path, err := chatEditorCmd(m.in.ed.String())
		if err != nil {
			m.in.hint = "editor: " + err.Error()
			return m, nil
		}
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			defer os.Remove(path)
			if err != nil {
				return chatEdited{err: err}
			}
			b, rerr := os.ReadFile(path)
			return chatEdited{text: string(b), err: rerr}
		})
	}
	if res.quit {
		m.saveTail(m.in.ed.String())
		return m, tea.Quit
	}
	return m, nil
}

// chatEditorCmd is $VISUAL or $EDITOR (vi without either) on a temporary file holding text.
func chatEditorCmd(text string) (*exec.Cmd, string, error) {
	f, err := os.CreateTemp("", "ptln-chat-*.md")
	if err != nil {
		return nil, "", err
	}
	_, werr := f.WriteString(text)
	f.Close()
	if werr != nil {
		os.Remove(f.Name())
		return nil, "", werr
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	args := strings.Fields(editor)
	return exec.Command(args[0], append(args[1:], f.Name())...), f.Name(), nil
}

// chatSaveDraft keeps an unsent draft for next time.
func chatSaveDraft(d string) {
	if strings.TrimSpace(d) != "" {
		_ = os.WriteFile(chatDraftPath(), []byte(d), 0o600)
	} else {
		_ = os.Remove(chatDraftPath())
	}
}

// chatPinAsTab opens the room in a tab named chat, or selects it when it is open (-S).
func chatPinAsTab() error {
	return tmuxCmd("new-window", "-S", "-n", "chat", "-t", tmuxSessionName+":", selfExe()+" chat --tab").Run()
}
