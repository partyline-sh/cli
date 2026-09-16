package main

import (
	"fmt"
	"strings"
)

// diffview.go — a unified diff as a thing the board can render and a reviewer can walk.
//
// THE GAP THIS CLOSES. The board's `r` on a Review card opened the PR in a BROWSER. That is a link,
// not a review gate: the product's central promise is "read what the agent wrote, keep what's
// right", and the reading happened on GitHub or not at all. A run with no PR (manual merge policy,
// or a failed PR open) had no reading surface anywhere — the exact case where looking at the code
// matters most.
//
// This file is the PURE half: parse `git diff` output into files and hunks, and render a window of
// it. No git, no terminal, no client — so every rule about what a reviewer sees is testable without
// a repo. The overlay and the git plumbing live in board_review_overlay.go.

// diffFile is one file's change, as the reviewer navigates it.
type diffFile struct {
	Path    string // the display path (the new side; the old one when deleted)
	OldPath string // set when renamed — "was internal/api/env.go"
	Adds    int
	Dels    int
	Binary  bool
	// Lines is the file's rendered body: hunk headers and +/-/context lines, verbatim from git,
	// unstyled. Styling happens at render time so the width clip cannot cut an escape sequence.
	Lines []string
}

// parseUnifiedDiff turns `git diff --no-color` output into files.
//
// Tolerant, never failing: a diff this cannot parse renders as one opaque file rather than an
// error, because the reviewer's alternative is no diff at all. Git's format is stable, but renames,
// mode changes and binary files all vary the header shape, and a parser that errored on any of
// them would fail exactly on the interesting changes.
func parseUnifiedDiff(text string) []diffFile {
	var files []diffFile
	var cur *diffFile
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &diffFile{Path: pathFromDiffHeader(line)}
		case cur == nil:
			continue // preamble before the first file header
		case strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
		case strings.HasPrefix(line, "rename from "):
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "+++ b/"):
			cur.Path = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "--- a/"):
			if cur.OldPath == "" {
				cur.OldPath = strings.TrimPrefix(line, "--- a/")
			}
		case strings.HasPrefix(line, "@@"):
			cur.Lines = append(cur.Lines, line)
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			cur.Adds++
			cur.Lines = append(cur.Lines, line)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			cur.Dels++
			cur.Lines = append(cur.Lines, line)
		case strings.HasPrefix(line, " ") || line == "":
			if len(cur.Lines) > 0 { // context before the first hunk header is git noise
				cur.Lines = append(cur.Lines, line)
			}
		}
	}
	flush()
	// A rename with no content change has a header and nothing else; OldPath == Path means the
	// --- / +++ pair was not a rename after all.
	for i := range files {
		if files[i].OldPath == files[i].Path {
			files[i].OldPath = ""
		}
	}
	return files
}

// pathFromDiffHeader pulls the b-side path out of `diff --git a/x b/x`.
func pathFromDiffHeader(line string) string {
	// The b/ path is the last field in the common case. Quoted paths (spaces, unicode) keep their
	// quotes — rare enough that rendering them quoted beats a real shell-unquoting pass here.
	if i := strings.LastIndex(line, " b/"); i >= 0 {
		return line[i+3:]
	}
	return strings.TrimPrefix(line, "diff --git ")
}

// ── the rendered document ────────────────────────────────────────────────────────────────────────

// diffDoc is the flattened, styled document the overlay windows over: one []string with a file
// index, so scrolling is a slice and jumping to a file is a lookup.
type diffDoc struct {
	Lines     []string
	FileStart []int    // Lines index where each file's header sits
	FilePaths []string // parallel to FileStart, for the position line
}

const (
	diffAddColor  = "\x1b[32m"
	diffDelColor  = "\x1b[31m"
	diffHunkColor = "\x1b[36m"
	diffFileBold  = "\x1b[1m"
	diffDim       = "\x1b[2m"
	diffReset     = "\x1b[0m"
)

// diffLineCap bounds a single file's rendered body. A generated lockfile or a vendored blob can be
// tens of thousands of lines; a reviewer paging through that IS the failure, so the tail is elided
// with a count and the advice to open the PR for the long read.
const diffLineCap = 2000

// buildDiffDoc renders files into the scrollable document.
func buildDiffDoc(files []diffFile) diffDoc {
	var d diffDoc
	if len(files) == 0 {
		d.Lines = []string{diffDim + "no changes between the base and this branch" + diffReset}
		return d
	}
	for _, f := range files {
		d.FileStart = append(d.FileStart, len(d.Lines))
		d.FilePaths = append(d.FilePaths, f.Path)
		head := diffFileBold + f.Path + diffReset
		if f.OldPath != "" {
			head += diffDim + "  (was " + f.OldPath + ")" + diffReset
		}
		head += fmt.Sprintf("  %s+%d%s %s-%d%s", diffAddColor, f.Adds, diffReset, diffDelColor, f.Dels, diffReset)
		d.Lines = append(d.Lines, head)
		if f.Binary {
			d.Lines = append(d.Lines, diffDim+"  binary file — not shown"+diffReset, "")
			continue
		}
		body := f.Lines
		elided := 0
		if len(body) > diffLineCap {
			elided = len(body) - diffLineCap
			body = body[:diffLineCap]
		}
		for _, l := range body {
			switch {
			case strings.HasPrefix(l, "@@"):
				d.Lines = append(d.Lines, diffHunkColor+l+diffReset)
			case strings.HasPrefix(l, "+"):
				d.Lines = append(d.Lines, diffAddColor+l+diffReset)
			case strings.HasPrefix(l, "-"):
				d.Lines = append(d.Lines, diffDelColor+l+diffReset)
			default:
				d.Lines = append(d.Lines, l)
			}
		}
		if elided > 0 {
			d.Lines = append(d.Lines, diffDim+fmt.Sprintf("  … %d more lines in this file — open the PR for the full read", elided)+diffReset)
		}
		d.Lines = append(d.Lines, "")
	}
	return d
}

// fileAt reports which file a document line falls in — the position line's "partyline/env.go (3/12)".
func (d diffDoc) fileAt(line int) int {
	idx := 0
	for i, start := range d.FileStart {
		if line >= start {
			idx = i
		}
	}
	return idx
}

// nextFile / prevFile return the scroll target for file-level navigation, clamped.
func (d diffDoc) nextFile(line int) int {
	for _, start := range d.FileStart {
		if start > line {
			return start
		}
	}
	return line
}

func (d diffDoc) prevFile(line int) int {
	prev := 0
	for _, start := range d.FileStart {
		if start >= line {
			break
		}
		prev = start
	}
	return prev
}

// diffStatLine is the one-line summary for the overlay title: "12 files · +340 −77".
func diffStatLine(files []diffFile) string {
	adds, dels := 0, 0
	for _, f := range files {
		adds += f.Adds
		dels += f.Dels
	}
	return fmt.Sprintf("%d %s · %s+%d%s %s−%d%s",
		len(files), plural(len(files), "file", "files"),
		diffAddColor, adds, diffReset, diffDelColor, dels, diffReset)
}
