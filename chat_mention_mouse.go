package main

// Mention rows are drawn immediately before the input. Match their actual
// clipped viewport coordinates, then use the same insertion path as Enter/Tab.
func (m *chatPopup) pickMentionAt(x, y int) bool {
	if len(m.in.menu) == 0 || x < m.margin() || x >= m.margin()+m.width() {
		return false
	}
	_, room, below, _, _, _ := m.layout(gateStyle{m.motion.theme()})
	helpY := room + len(below)
	start := helpY - len(m.in.menu)
	if y < max(start, room+1) || y >= helpY {
		return false
	}
	index := y - start
	if index < 0 || index >= len(m.in.menu) {
		return false
	}
	m.in.menuAt = index
	m.in.menuKey(key{kind: keyEnter})
	m.selection = chatSelection{}
	m.saveTail(m.in.ed.String())
	return true
}
