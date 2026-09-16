package main

import "testing"

// `ptln login <url>` fetches the instance's identity key over that connection and pins it. TOFU is
// only safe because TLS authenticates the answer — over plain http anyone on the path can hand you
// their own key and become the thing your host believes about who is joining.
func TestValidInstanceURL(t *testing.T) {
	ok := []string{
		"https://partyline.sh",
		"https://ptln.example.com",
		"https://ptln.example.com:8443",
		"http://localhost:3111", // loopback: no network to sit on, and it's how you test your own box
		"http://127.0.0.1:3111",
	}
	for _, u := range ok {
		if err := validInstanceURL(u); err != nil {
			t.Errorf("%s: rejected, want accepted (%v)", u, err)
		}
	}
	bad := []string{
		"http://ptln.example.com", // the whole point: plaintext key fetch
		"ftp://ptln.example.com",
		"ptln.example.com", // no scheme — url.Parse gives an empty Host
		"",
		"https://", // no host
	}
	for _, u := range bad {
		if err := validInstanceURL(u); err == nil {
			t.Errorf("%q: accepted, want rejected", u)
		}
	}
}
