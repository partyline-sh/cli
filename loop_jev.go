package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Jev (typesafe.ai) gives an outside opinion when the models disagree: a fast model that answers
// typed questions with calibrated probabilities. It advises; the models decide. With no key saved
// there is no Jev; with one that fails (no credits, a revoked key, a timeout, any error) the
// dispute goes on without it. The key is kept like the Cloudflare one: ~/.partyline/jev-key, 0600,
// which loop builders cannot read.

const jevURL = "https://api.typesafe.ai/v1/systemone"

func jevKeyPath() string { return filepath.Join(stateDir(), "jev-key") }

func savedJevKey() string {
	b, err := os.ReadFile(jevKeyPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveJevKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("a Jev API key is one word with no spaces")
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	return relayPrivateFile(jevKeyPath(), []byte(key+"\n"))
}

// jevHTTP and jevEndpoint are variables for tests.
var (
	jevHTTP     = &http.Client{Timeout: 15 * time.Second}
	jevEndpoint = jevURL
)

// jevFindingStands asks Jev how likely a disputed finding is right: ok is false when there is no
// key or Jev did not answer, and the dispute goes on without it.
func jevFindingStands(ctx context.Context, brief string, t loopTracked) (float64, bool) {
	key := savedJevKey()
	if key == "" {
		return 0, false
	}
	// Short state: Jev's own guidance is that it is less reliable on long context.
	if len(brief) > 6000 {
		brief = brief[:6000] + "…"
	}
	body, _ := json.Marshal(map[string]any{
		"model": "jev-latest",
		"state": map[string]string{"brief": brief, "finding": formatTracked([]loopTracked{t}), "author_dispute": t.Dispute},
		"questions": map[string]any{
			"stands": map[string]string{
				"type":         "noul",
				"instructions": "The reviewer's finding is correct: the work must change to address it, and the author's dispute does not hold.",
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", jevEndpoint, bytes.NewReader(body))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := jevHTTP.Do(req)
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, false // no credits, a bad key, a rate limit: skip Jev
	}
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil {
		return 0, false
	}
	a, ok := out.Answers["stands"]
	if !ok || a.Noul == nil || *a.Noul < 0 || *a.Noul > 1 {
		return 0, false
	}
	return *a.Noul, true
}
