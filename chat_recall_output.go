package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func boundChatRecall(page []chatMessage, search string) {
	remaining := 64 << 10
	for i := range page {
		text := page[i].Text
		limit := min(8192, remaining)
		if len(text) > limit {
			start := 0
			if search != "" && limit > 0 {
				if at := strings.Index(strings.ToLower(text), search); at > limit/2 {
					start = min(at-limit/2, len(text)-limit)
				}
			}
			for start < len(text) && !utf8.RuneStart(text[start]) {
				start++
			}
			end := min(start+limit, len(text))
			for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
				end--
			}
			page[i].Text = text[start:end] + fmt.Sprintf("\n[excerpt; narrow with --around %d --limit 1 or --search]", page[i].Seq)
		}
		remaining -= min(len(text), limit)
	}
}
