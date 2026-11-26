package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// remoteChatLimit sets the chat room's agent reply limit for a paired phone. Callers hold the
// phone lock and have checked the phone is paired; the input store caches the result by id, so
// a retry repeats the answer instead of the change. The phone may raise or lower the limit but
// not turn it off: the limit is what stops agents answering each other unattended. Turning it
// off stays on the computer (/limit off).
func remoteChatLimit(route string, m remote.Message, now time.Time) (status, reason string, data any) {
	var body struct {
		Limit *float64 `json:"limit"`
	}
	raw, err := json.Marshal(m.Data)
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	if err != nil || body.Limit == nil || *body.Limit != float64(int(*body.Limit)) {
		return "refused", "limit must be a whole number", nil
	}
	limit := int(*body.Limit)
	if limit < 4 || limit > 1000000 {
		return "refused", "limit must be 4–1000000; turn it off on the computer", nil
	}
	// Audit before the change: an unaudited remote change must not happen.
	if err := remoteChatLimitAudit(route, m.ID, limit, now); err != nil {
		return "refused", "audit: " + err.Error(), nil
	}
	if err := chatSetLimit(limit); err != nil {
		return "refused", err.Error(), nil
	}
	r := loadChatRoom()
	return "ok", "", map[string]any{"limit": r.cap(), "agentRun": r.AgentRun, "stopped": r.Stopped}
}

func remoteChatLimitAudit(route, id string, limit int, now time.Time) error {
	f, err := os.OpenFile(filepath.Join(relayDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(map[string]any{"at": now.UTC(), "action": "chat_limit", "inputId": id, "phoneId": route, "limit": limit}); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return errors.New("audit not durable")
	}
	return nil
}
