package main

// This metadata travels inside the authenticated phone/computer channel. It
// describes only the computer's configured route, never owner credentials or
// other members of the gateway. Missing metadata is supported by older phones.
func remoteRelayMetadata() map[string]string {
	c, _, err := loadRelay()
	if err != nil {
		return nil
	}
	info := map[string]string{"origin": c.URL}
	if c.Hosting == "self-hosted" || c.Hosting == "cloudflare" {
		info["hosting"] = c.Hosting
	}
	return info
}
