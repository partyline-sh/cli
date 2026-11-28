package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func chatRecallExclusionsPath() string { return filepath.Join(chatDir(), "recall-excluded.json") }

// Message-number exclusions handle known sensitive messages that have no
// recognizable key format or password label. The source log is never rewritten.
func loadChatRecallExclusions() (map[int]bool, error) {
	data, err := os.ReadFile(chatRecallExclusionsPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot read recall exclusions; refusing history")
	}
	if len(data) > 64<<10 {
		return nil, errors.New("recall exclusions too large; refusing history")
	}
	var ids []int
	if json.Unmarshal(data, &ids) != nil {
		return nil, errors.New("invalid recall exclusions; refusing history")
	}
	hidden := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id < 1 {
			return nil, errors.New("invalid excluded message number; refusing history")
		}
		hidden[id] = true
	}
	return hidden, nil
}
