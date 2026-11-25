package main

// Markdown for the chat room: goldmark parses it, and this turns the tree into terminal text.
//
// glamour does the same job out of the box but brings a syntax highlighter that doubled the binary
// (8.2 MB to 16.5 MB), which every partyline install would pay for chat formatting. Parsing is the
// part that has to be right and goldmark does it; drawing the tree is a short list of cases.
//
// Supported: paragraphs, headings, emphasis, strikethrough, inline code, code blocks, ordered and
// nested lists, quotes, rules, links, autolinks and tables. Raw HTML is shown as written.

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrItalic = "\x1b[3m"
	sgrDim    = "\x1b[2m"
	sgrStrike = "\x1b[9m"
)

var chatMD = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify))

// renderMarkdown renders src for a terminal width, every line starting with indent.
func renderMarkdown(src, indent string, width int) string {
	source := []byte(src)
	doc := chatMD.Parser().Parse(text.NewReader(source))
	r := &mdRender{src: source, width: width}
	r.blocks(doc, indent, indent)
	return strings.TrimRight(r.out.String(), "\n") + "\n"
}

type mdRender struct {
	src   []byte
	width int
	out   strings.Builder
}

// blocks renders each block child of n. first is the prefix for the first line of the first block
// (a list bullet), rest the prefix for everything after.
func (r *mdRender) blocks(n ast.Node, first, rest string) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c != n.FirstChild() && blockGap(c) {
			r.out.WriteString("\n")
		}
		r.block(c, first, rest)
		first = rest
	}
}

// blockGap reports whether a blank line goes before this block. Items of a tight list do not get
// one; everything else does.
func blockGap(n ast.Node) bool {
	if p, ok := n.Parent().(*ast.ListItem); ok {
		if l, ok := p.Parent().(*ast.List); ok && l.IsTight {
			return false
		}
	}
	return true
}

func (r *mdRender) block(n ast.Node, first, rest string) {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		r.wrap(r.inline(n), first, rest)
	case *ast.Heading:
		r.wrap(tmuxTheme().ChatHeading()+r.inline(n)+sgrReset, first, rest)
	case *ast.List:
		num := v.Start
		for item := v.FirstChild(); item != nil; item = item.NextSibling() {
			bullet := "• "
			if v.IsOrdered() {
				bullet = strconv.Itoa(num) + ". "
				num++
			}
			pad := strings.Repeat(" ", runewidth.StringWidth(bullet))
			r.blocks(item, rest+bullet, rest+pad)
			if !v.IsTight && item.NextSibling() != nil {
				r.out.WriteString("\n")
			}
		}
	case *ast.Blockquote:
		q := sgrDim + "│ " + sgrReset
		r.blocks(n, first+q, rest+q)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			line := strings.TrimRight(string(seg.Value(r.src)), "\n")
			r.out.WriteString(rest + "  " + tmuxTheme().ChatCode() + line + sgrReset + "\n")
		}
	case *ast.ThematicBreak:
		w := r.width - runewidth.StringWidth(stripColours(rest))
		r.out.WriteString(rest + sgrDim + strings.Repeat("─", max(3, min(w, 40))) + sgrReset + "\n")
	case *east.Table:
		r.table(v, rest)
	case *ast.HTMLBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			r.out.WriteString(rest + strings.TrimRight(string(seg.Value(r.src)), "\n") + "\n")
		}
	default:
		if n.HasChildren() {
			r.blocks(n, first, rest)
		}
	}
}

// inline renders the inline children of n as one styled string, with "\n" for hard breaks.
func (r *mdRender) inline(n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(r.src))
			if v.HardLineBreak() {
				b.WriteString("\n")
			} else if v.SoftLineBreak() {
				b.WriteString(" ")
			}
		case *ast.String:
			b.Write(v.Value)
		case *ast.CodeSpan:
			b.WriteString(tmuxTheme().ChatCode() + string(nodeText(v, r.src)) + sgrReset)
		case *ast.Emphasis:
			style := sgrItalic
			if v.Level >= 2 {
				style = sgrBold
			}
			b.WriteString(style + r.inline(v) + sgrReset)
		case *east.Strikethrough:
			b.WriteString(sgrStrike + r.inline(v) + sgrReset)
		case *ast.Link:
			label := r.inline(v)
			url := string(v.Destination)
			if stripColours(label) == url {
				b.WriteString("\x1b[4m" + url + sgrReset)
			} else {
				b.WriteString(sgrBold + label + sgrReset + " " + sgrDim + url + sgrReset)
			}
		case *ast.AutoLink:
			b.WriteString("\x1b[4m" + string(v.URL(r.src)) + sgrReset)
		case *ast.Image:
			b.WriteString(sgrDim + "[image: " + r.inline(v) + "] " + string(v.Destination) + sgrReset)
		case *ast.RawHTML:
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				b.Write(seg.Value(r.src))
			}
		default:
			b.WriteString(r.inline(c))
		}
	}
	return b.String()
}

// nodeText is the raw text under an inline node (a code span's contents).
func nodeText(n ast.Node, src []byte) []byte {
	var b bytes.Buffer
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
		}
	}
	return b.Bytes()
}

// wrap writes styled text word-wrapped to the width, the first line after first and the rest
// after rest. Width is measured on the visible text, so colour codes do not count.
func (r *mdRender) wrap(s, first, rest string) {
	prefix := first
	for _, para := range strings.Split(s, "\n") {
		avail := r.width - runewidth.StringWidth(stripColours(prefix))
		line, lineW := "", 0
		for _, w := range strings.Fields(para) {
			ww := runewidth.StringWidth(stripColours(w))
			if lineW > 0 && lineW+1+ww > avail {
				r.out.WriteString(prefix + line + sgrReset + "\n")
				prefix = rest
				avail = r.width - runewidth.StringWidth(stripColours(prefix))
				// A style that spans the break (a bold phrase) carries on after the new prefix.
				line, lineW = activeStyle(line), 0
			}
			if lineW > 0 {
				line += " "
				lineW++
			}
			line += w
			lineW += ww
		}
		r.out.WriteString(prefix + line + sgrReset + "\n")
		prefix = rest
	}
}

// activeStyle returns the colour codes still in effect at the end of s: those after its last reset.
func activeStyle(s string) string {
	var on []string
	for len(s) > 0 {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			break
		}
		end := strings.IndexByte(s[i:], 'm')
		if end < 0 {
			break
		}
		code := s[i : i+end+1]
		if code == sgrReset {
			on = on[:0]
		} else {
			on = append(on, code)
		}
		s = s[i+end+1:]
	}
	return strings.Join(on, "")
}

// table draws a table with columns sized to their widest cell, shrinking the widest columns when
// the whole table would not fit.
func (r *mdRender) table(t *east.Table, indent string) {
	var rows [][]string
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, strings.TrimSpace(r.inline(cell)))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], runewidth.StringWidth(stripColours(c)))
		}
	}
	avail := r.width - runewidth.StringWidth(stripColours(indent)) - 3*(cols-1)
	for sum(widths) > avail && avail > cols*4 {
		widest := 0
		for i := range widths {
			if widths[i] > widths[widest] {
				widest = i
			}
		}
		widths[widest]--
	}
	for ri, row := range rows {
		var parts []string
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(row) {
				c = row[i]
			}
			if ri == 0 {
				c = sgrBold + stripColours(c) + sgrReset
			}
			parts = append(parts, fitCell(c, widths[i]))
		}
		r.out.WriteString(indent + strings.Join(parts, sgrDim+" │ "+sgrReset) + "\n")
		if ri == 0 {
			var rule []string
			for _, w := range widths {
				rule = append(rule, strings.Repeat("─", w))
			}
			r.out.WriteString(indent + sgrDim + strings.Join(rule, "─┼─") + sgrReset + "\n")
		}
	}
}

// fitCell pads a styled cell to w columns, cutting it with "…" when it is wider.
func fitCell(c string, w int) string {
	plain := stripColours(c)
	if pw := runewidth.StringWidth(plain); pw <= w {
		return c + strings.Repeat(" ", w-pw)
	}
	return runewidth.Truncate(plain, w, "…")
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}
