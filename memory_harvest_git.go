package main

// Reading commits out of a local checkout. Nothing here looks at a diff or a file's contents —
// only the message, the author and the hash.

import (
	"strconv"
	"strings"
	"time"
)

// commitInfo is one commit, reduced to what memory cares about.
type commitInfo struct {
	SHA     string
	Author  string
	At      time.Time
	Message string
}

// harvestFieldSep and harvestRecSep are byte sequences a commit message will not contain, so a
// message with blank lines, colons or its own delimiters cannot break the parse. Splitting git
// output on newlines is the classic way to mangle multi-paragraph commit messages.
const (
	harvestFieldSep = "\x1f"
	harvestRecSep   = "\x1e"
)

// readCommits returns commits in the given range, newest last. `rev` is any git revision range
// ("abc123..HEAD"); empty means the whole history, which is why callers bound it.
func readCommits(repoPath, rev string, limit int) ([]commitInfo, error) {
	args := []string{"log", "--no-merges", "--reverse",
		"--pretty=format:%H" + harvestFieldSep + "%an" + harvestFieldSep + "%aI" + harvestFieldSep + "%B" + harvestRecSep}
	if limit > 0 {
		args = append(args, "--max-count="+strconv.Itoa(limit))
	}
	if strings.TrimSpace(rev) != "" {
		args = append(args, rev)
	}
	out, err := gitMem(repoPath, args...)
	if err != nil {
		return nil, err
	}
	var commits []commitInfo
	for _, rec := range strings.Split(out, harvestRecSep) {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		parts := strings.SplitN(rec, harvestFieldSep, 4)
		if len(parts) < 4 {
			continue
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[2]))
		if err != nil {
			at = time.Now()
		}
		commits = append(commits, commitInfo{
			SHA:     strings.TrimSpace(parts[0]),
			Author:  strings.TrimSpace(parts[1]),
			At:      at,
			Message: parts[3],
		})
	}
	return commits, nil
}
