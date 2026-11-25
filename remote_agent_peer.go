package main

import "strings"

// Older clients cannot bind a chat send to an adopted process. Keep their shell
// presentation until they opt in, rather than turning a queued chat into shell input.
func remoteSnapshotForPeer(topic, mode string, detect bool) (any, error) {
	if !detect && strings.HasPrefix(topic, "transcript:") {
		inventory, err := remoteSnapshot("sessions", "focus")
		if err != nil {
			return nil, err
		}
		for _, row := range inventory.([]map[string]string) {
			if row["pane"] == strings.TrimPrefix(topic, "transcript:") && row["detected"] == "true" {
				return nil, nil
			}
		}
	}
	data, err := remoteSnapshot(topic, mode)
	if err != nil || topic != "sessions" || detect {
		return data, err
	}
	for _, row := range data.([]map[string]string) {
		if row["detected"] == "true" {
			row["engine"] = ""
			delete(row, "detected")
			delete(row, "agentTarget")
		}
	}
	return data, nil
}
