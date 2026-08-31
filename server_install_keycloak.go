package main

// The sign-in client lives in Keycloak's DATABASE. env-bootstrap.sh writes the realm import
// file once (only-if-absent) and Keycloak reads it once (first boot of an empty realm) — so a
// changed site address never reaches the live client, and sign-in dies with Keycloak's
// "Invalid parameter: redirect_uri" while everything else looks healthy. Observed on a real
// box; fixed there by hand with kcadm. This is that fix as a pipeline step: idempotent,
// re-run on every install/upgrade, so the client always matches the current site.

import (
	"fmt"
	"strings"
	"time"
)

// kcadmSyncSignIn points the partyline-web client's redirect URI and web origin at the site.
// Admin credentials stay inside the container: the shell there reads them from its own
// environment, so no secret value ever passes through our argv or output.
func kcadmSyncSignIn(dir, site string, ops installOps) error {
	site = strings.TrimRight(site, "/")
	if ops.sleep == nil {
		ops.sleep = time.Sleep
	}
	script := `set -e
/opt/keycloak/bin/kcadm.sh config credentials --server http://localhost:8080/auth --realm master --user "$KC_BOOTSTRAP_ADMIN_USERNAME" --password "$KC_BOOTSTRAP_ADMIN_PASSWORD" >/dev/null
CID=$(/opt/keycloak/bin/kcadm.sh get clients -r partyline -q clientId=partyline-web --fields id --format csv --noquotes | head -1)
[ -n "$CID" ] || { echo "no partyline-web client in the realm"; exit 1; }
/opt/keycloak/bin/kcadm.sh update clients/$CID -r partyline -s 'redirectUris=["` + site + `/api/auth/callback"]' -s 'webOrigins=["` + site + `"]'
/opt/keycloak/bin/kcadm.sh get clients/$CID -r partyline --fields redirectUris`
	// Keycloak may still be booting right after `up -d` — importing a realm on first start
	// takes it tens of seconds. Retry rather than fail the whole install on a race.
	var out string
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		out, err = ops.run(dir, "docker", "compose", "exec", "-T", "keycloak", "sh", "-c", script)
		if err == nil {
			break
		}
		ops.sleep(5 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("could not update the sign-in client: %s", installFirstLine(out))
	}
	if !strings.Contains(out, site+"/api/auth/callback") {
		return fmt.Errorf("the sign-in client did not take the new redirect URI")
	}
	return nil
}
