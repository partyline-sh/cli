package features

import "sort"

// Every environment variable the code reads must be EITHER declared in features.json as gating a
// feature, or classified here with the reason it is not one. That closing rule is what makes the
// registry stay true: a new `process.env.NEW_THING` that gates a feature cannot be added without
// someone either declaring it or writing down why it is not a gate. It is the same shape as
// docsaudit.RequiresDoc — an exemption you have to state out loud, not an omission you can drift
// into. drift.go enforces both directions.
//
// This table is also where the non-feature half of deploy/stack/env.example comes from, so the
// generated file still tells an operator how to bring a box up, not just which features exist.

// Class says why a variable is not a feature gate.
type Class string

const (
	// Core: the server does not run correctly without it. Not a feature — there is no state in
	// which the app is up and this is legitimately unset.
	Core Class = "core"
	// Compose: read by docker compose or the init scripts, never by application code. Belongs in
	// the box's .env but can never appear in the extractor's output.
	Compose Class = "compose"
	// Optional: tunes or provides a fallback for something already declared. Unset is a supported
	// state with a documented default, so it is not part of any feature's configured/not answer.
	Optional Class = "optional"
	// Client: read by the CLI on a user's own machine. Not server configuration at all, and must
	// never appear in the deploy env example.
	Client Class = "client"
)

// NonFeature is one classified variable. Why is prose for a human; Example is the line the
// generated env.example carries (empty means "no default — fill it in").
type NonFeature struct {
	Name    string
	Class   Class
	Why     string
	Example string
}

// NonFeatures is the classification table. Keep it alphabetical within each class.
var NonFeatures = []NonFeature{
	// ---- core: the app is broken without these ----
	{Name: "NEXT_PUBLIC_SITE_URL", Class: Core, Why: "the site origin the browser bundle links to", Example: "https://staging.partyline.sh"},
	{Name: "NEXT_PUBLIC_SUPABASE_ANON_KEY", Class: Core, Why: "the legacy name for the anon PostgREST key, still read as a fallback so a box whose .env predates the rename keeps working; set PGRST_ANON_KEY on a new box"},
	{Name: "NEXT_PUBLIC_SUPABASE_URL", Class: Core, Why: "the legacy name for PostgREST's origin, still read as a fallback so a box whose .env predates the rename keeps working; set PGRST_URL on a new box"},
	{Name: "NODE_ENV", Class: Core, Why: "set by Next itself; production/development"},
	{Name: "PGRST_ANON_KEY", Class: Core, Why: "the anon JWT the server-side PostgREST client presents"},
	{Name: "PGRST_URL", Class: Core, Why: "PostgREST's origin, read at RUNTIME so one image can serve both boxes", Example: "http://postgrest:3000"},
	{Name: "SESSION_JWT_SECRET", Class: Core, Why: "signs our session JWTs; must be byte-identical to PGRST_JWT_SECRET and TRIMMED — a trailing newline mints tokens PostgREST silently rejects"},
	{Name: "SITE_URL", Class: Core, Why: "the canonical origin every server-rendered link and email is built from", Example: "https://staging.partyline.sh"},
	{Name: "SUPABASE_SERVICE_ROLE_KEY", Class: Core, Why: "the admin bypass every adminClient() call uses — treat as root"},

	// ---- compose: read by the stack, never by application code ----
	{Name: "AUTHENTICATOR_PASSWORD", Class: Compose, Why: "PostgREST's login role; must match the bootstrap in deploy/stack/init/"},
	// ---- where the stack listens. Nothing hardcodes 80/443 any more: a self-hoster installs onto
	// a box that already does something, and "port 80 is taken" used to end the install. Every
	// default below is the literal it replaced, so an existing box is unaffected by their presence.
	{Name: "BIND_ADDR", Class: Compose, Why: "the host interface every published port binds to; unset = 0.0.0.0, which is what an unqualified \"80:80\" already meant. Set 127.0.0.1, a LAN address or a Tailscale IP to stop publishing on every NIC", Example: "0.0.0.0"},
	{Name: "CADDY_IMAGE", Class: Compose, Why: "overrides the WHOLE Caddy image reference. The stock image has no DNS provider modules, so this is how you run a build that does — the only route to a real certificate on a box the public internet cannot reach (DNS-01 proves you own the name, not that the box is reachable)", Example: ""},
	{Name: "CADDY_REPLICAS", Class: Compose, Why: "set 0 to stop running the bundled Caddy — the no-reverse-proxy mode, with docker-compose.direct.yml; unset = 1", Example: "1"},
	{Name: "NEXT_RUNTIME", Class: Optional, Why: "set by Next itself per runtime; read to keep the self-tick out of the edge runtime"},
	{Name: "PARTYLINE_SELF_TICK", Class: Optional, Why: "set to 1 to have the web process POST its own 60s maintenance tick (the compose stack sets it — it replaced the ticker container). Unset = an external scheduler is expected to hit /api/v1/tick"},
	{Name: "PARTYLINE_MARKETING_ONLY", Class: Optional, Why: "set to 1 to serve ONLY the documentation and marketing pages, 404ing the application half — this is what partyline.sh itself runs, and it is the opposite of what a self-hoster wants. LEAVE IT UNSET: setting it on your own instance turns the product off", Example: ""},
	{Name: "HTTP_PORT", Class: Compose, Why: "the host port Caddy publishes for HTTP; unset = 80. Moving it means Let's Encrypt can no longer validate this box, so use it only behind another proxy", Example: "80"},
	{Name: "HTTPS_PORT", Class: Compose, Why: "the host port Caddy publishes for HTTPS; unset = 443. Same caveat as HTTP_PORT", Example: "443"},
	{Name: "KEYCLOAK_PORT", Class: Compose, Why: "with docker-compose.direct.yml, the host port Keycloak publishes — sign-in is a browser redirect to it, so it needs one when there is no proxy; unset = 8081", Example: ""},
	{Name: "MINIO_REPLICAS", Class: Compose, Why: "set 0 to stop running the bundled MinIO on a box that points S3_* at R2 or S3; unset = 1", Example: "1"},
	{Name: "MINIO_ROOT_PASSWORD", Class: Compose, Why: "the bundled MinIO's root password, generated by scripts/env-bootstrap.sh — never a default"},
	{Name: "MINIO_ROOT_USER", Class: Compose, Why: "the bundled MinIO's root user, generated by scripts/env-bootstrap.sh", Example: "partyline"},
	{Name: "POSTGRES_PASSWORD", Class: Compose, Why: "the database superuser password (openssl rand -base64 32)"},
	{Name: "OIDC_PUBLIC_URL", Class: Compose, Why: "the base URL a BROWSER reaches Keycloak on, including /auth; compose passes it as KC_HOSTNAME. Unset = SITE_URL/auth, which is right behind Caddy. It must equal OIDC_ISSUER minus /realms/<realm> or discovery refuses the document", Example: ""},
	{Name: "RELAY_IMAGE", Class: Compose, Why: "overrides the WHOLE relay image reference; the only way to pin a digest, since @ is illegal in a tag. Unset = whatever RELAY_TAG names"},
	{Name: "RELAY_TAG", Class: Compose, Why: "the relay image tag compose runs; upserted by the deploy workflow alongside WEB_TAG", Example: "latest"},
	{Name: "RELAY_PORT", Class: Compose, Why: "the host port the relay publishes; unset = 2222. It used to be 22 on our own box because a relay hostname was compiled into the CLI — nothing is now, so pick whatever does not collide with sshd", Example: "2222"},
	{Name: "WEB_IMAGE", Class: Compose, Why: "overrides the WHOLE web image reference, e.g. ghcr.io/partyline-sh/partyline-web@sha256:… or your own mirror. Unset = whatever WEB_TAG names", Example: ""},
	{Name: "WEB_PORT", Class: Compose, Why: "with docker-compose.direct.yml, the host port the web app publishes directly; unset = 8080. Ignored when Caddy is the edge", Example: ""},
	{Name: "WEB_TAG", Class: Compose, Why: "the image tag compose runs; upserted by the deploy workflow, set by hand to pin or roll back. The images are public — see deploy/stack/README.md for pinning", Example: "latest"},

	// ---- optional: a supported unset state with a documented default ----
	// Selects the sign-in adapter behind the auth seam (H.3a). OPTIONAL rather than Core because
	// there is exactly one adapter now — oidc — and an unset value already means it. It is kept as a
	// named seam so a second provider is a new implementation rather than an edit to the two auth
	// routes, not because anybody has to set it.
	{Name: "AUTH_PROVIDER", Class: Optional, Why: "which sign-in adapter to use; unset = oidc, which is the only one", Example: "oidc"},
	{Name: "OIDC_ALLOW_INSECURE_ISSUER", Class: Optional, Why: "accept a plain-http OIDC_ISSUER on a non-loopback host; unset = refuse, which is the only safe default on a public network. Set it ONLY on a private network (LAN, VPN, Tailscale) that has no certificate — over http the JWKS can be rewritten in flight and a rewritten JWKS verifies a forged token"},
	{Name: "OIDC_REDIRECT_URI", Class: Optional, Why: "the generic OIDC callback; falls back to SITE_URL + /api/auth/callback"},
	{Name: "OIDC_SCOPES", Class: Optional, Why: "scopes requested from the OIDC issuer; unset = \"openid email profile\", which is what the profile mapping needs"},
	{Name: "PARTYLINE_CLI_NOTICE", Class: Optional, Why: "one-line notice shown to CLIs on their update check; empty = none"},
	{Name: "PARTYLINE_MIN_CLI", Class: Optional, Why: "minimum supported CLI version; unset = 0.0.0, nothing is gated"},
	// The review host's port. It is a LOCAL, loopback-only listener on a developer's own machine —
	// not a service anyone deploys — so it gates nothing and belongs here rather than in
	// features.json. Fixed by default (7391) because the URL has to be predictable: an agent builds
	// it from a work item id alone. Override it only when 7391 is already taken.
	{Name: "PARTYLINE_REVIEW_PORT", Class: Optional, Why: "local port the review host listens on (127.0.0.1 only); unset = 7391", Example: "7391"},
	{Name: "PARTYLINE_SCRIBE_MODEL", Class: Optional, Why: "overrides the scribe's default model"},
	{Name: "PARTYLINE_SCRIBE_PROVIDER", Class: Optional, Why: "overrides the scribe's default provider (anthropic)"},
	{Name: "R2_ACCESS_KEY_ID", Class: Optional, Why: "legacy name still honoured as a fallback for S3_ACCESS_KEY_ID; do not set on a new box"},
	{Name: "R2_BUCKET", Class: Optional, Why: "legacy name still honoured as a fallback for S3_BUCKET; do not set on a new box"},
	{Name: "R2_ENDPOINT", Class: Optional, Why: "legacy name still honoured as a fallback for S3_ENDPOINT; do not set on a new box"},
	{Name: "R2_SECRET_ACCESS_KEY", Class: Optional, Why: "legacy name still honoured as a fallback for S3_SECRET_ACCESS_KEY; do not set on a new box"},
	{Name: "S3_FORCE_PATH_STYLE", Class: Optional, Why: "overrides object-storage addressing; unset = path style for a bare host (MinIO), virtual-hosted otherwise"},
	{Name: "S3_REGION", Class: Optional, Why: "object-storage region; unset = auto, which R2 and MinIO accept. Real AWS S3 needs the bucket's region"},
	{Name: "SLACK_STATE_SECRET", Class: Optional, Why: "signs the Slack OAuth CSRF state; falls back to SESSION_JWT_SECRET, which is the #175 footgun — set it to finish the split"},
	{Name: "SUPABASE_JWT_SECRET", Class: Optional, Why: "legacy name still honoured as a fallback for SLACK_STATE_SECRET; do not set on a new box"},

	// ---- client: the CLI on a user's machine, never server configuration ----
	{Name: "ALACRITTY_SOCKET", Class: Client, Why: "terminal detection for opening a session in a new window"},
	{Name: "ALACRITTY_WINDOW_ID", Class: Client, Why: "terminal detection"},
	{Name: "CI", Class: Client, Why: "suppresses the telemetry notice and the update check in CI"},
	// Not server configuration and deliberately NOT in the box's .env: it says where the .env
	// itself lives, so it has to be set in the shell before anything reads that file.
	// scripts/env-bootstrap.sh and `ptln server bootstrap` read the same variable, which is what
	// makes the script's install directory and the command's printed plan agree.
	{Name: "PARTYLINE_DIR", Class: Client, Why: "where the self-hosted stack is installed; unset = /opt/partyline"},
	{Name: "CLAUDE_CODE_SESSION_ID", Class: Client, Why: "set by claude; identifies the session an agent is speaking from"},
	{Name: "COLORTERM", Class: Client, Why: "truecolor detection for the wordmark"},
	{Name: "DO_NOT_TRACK", Class: Client, Why: "the standard telemetry opt-out, honoured by the CLI"},
	{Name: "EDITOR", Class: Client, Why: "the editor ctrl-e opens for a long message"},
	{Name: "GNOME_TERMINAL_SCREEN", Class: Client, Why: "terminal detection"},
	{Name: "KITTY_WINDOW_ID", Class: Client, Why: "terminal detection"},
	{Name: "KONSOLE_VERSION", Class: Client, Why: "terminal detection"},
	{Name: "NO_COLOR", Class: Client, Why: "the standard colour opt-out"},
	{Name: "NO_UPDATE_NOTIFIER", Class: Client, Why: "the conventional opt-out for update nagging"},
	{Name: "PAGER", Class: Client, Why: "pager for `ptln man` and long TUI output"},
	{Name: "PARTYLINE", Class: Client, Why: "marker exported into a shared shell so a nested ptln knows it is inside one"},
	{Name: "PARTYLINE_AGENT_NAME", Class: Client, Why: "the name an MCP-wired agent reports itself as"},
	{Name: "PARTYLINE_ALLOW_TEMP_CONNECT", Class: Client, Why: "dev escape hatch for connecting a thread without an account"},
	{Name: "PARTYLINE_API", Class: Client, Why: "points the CLI at a different control plane (local dev, staging)"},
	{Name: "PARTYLINE_CHECKUP_IDLE_SECS", Class: Client, Why: "mux idle-checkup tuning"},
	{Name: "PARTYLINE_CHECKUP_POLL_SECS", Class: Client, Why: "mux idle-checkup tuning"},
	{Name: "PARTYLINE_CRANK_WORKERS", Class: Client, Why: "default worker count for crank/daemon on this machine"},
	{Name: "PARTYLINE_DAEMON_TOKEN", Class: Client, Why: "the daemon's own token, handed to workers it spawns"},
	{Name: "PARTYLINE_ENGINE", Class: Client, Why: "which engine an MCP-wired session is running"},
	{Name: "PARTYLINE_TMUX_SOCKET", Class: Client, Why: "override the tmux backend's socket name (tests/scripts; the default socket is per-user machine-wide)"},
	{Name: "NODE_EXTRA_CA_CERTS", Class: Compose, Why: "points Node at Caddy's internal root certificate (mounted read-only at /caddy-pki) so server-side OIDC fetches trust the box's own CA; the installer writes it in tls-internal mode, empty otherwise", Example: ""},
	{Name: "SUDO_USER", Class: Client, Why: "set by sudo itself, never by an operator: the installer reads it to record the install directory in the INVOKING user's home too, so their unprivileged day-2 commands find a sudo-made install"},
	{Name: "PARTYLINE_MUX", Class: Client, Why: "session host selection: \"classic\" opts out of the tmux backend (the default when tmux 3.3+ is installed)"},
	{Name: "PARTYLINE_MAX_TOKENS", Class: Client, Why: "token ceiling for a crank run"},
	{Name: "PARTYLINE_NO_CODEX_MCP", Class: Client, Why: "turns OFF the partyline MCP for codex party turns (#556 un-gated it by default); claude is unaffected, unlike the global PARTYLINE_NO_MCP"},
	{Name: "PARTYLINE_NAME", Class: Client, Why: "display name for this machine's sessions"},
	{Name: "PARTYLINE_NO_CONNECT_PROMPT", Class: Client, Why: "suppresses the first-run MCP connect prompt"},
	{Name: "PARTYLINE_NO_MCP", Class: Client, Why: "runs a party agent with no MCP servers at all"},
	{Name: "PARTYLINE_NO_UPDATE_CHECK", Class: Client, Why: "suppresses the CLI update check"},
	{Name: "PARTYLINE_PARTY_ID", Class: Client, Why: "the party an MCP-wired agent belongs to"},
	{Name: "PARTYLINE_PARTY_TOKEN", Class: Client, Why: "that agent's party token"},
	{Name: "PARTYLINE_PARTY_HUMAN", Class: Client, Why: "marks an MCP session a PERSON sits in, so their posts use their login identity"},
	{Name: "PARTYLINE_RELAY", Class: Client, Why: "points the CLI at a different relay host"},
	{Name: "PARTYLINE_RUN_ID", Class: Client, Why: "the control-plane run a worker is reporting to"},
	{Name: "PARTYLINE_SESSION_KEY", Class: Client, Why: "the session key an MCP-wired agent authenticates with"},
	{Name: "PARTYLINE_SESSION_LABEL", Class: Client, Why: "the label ask_session publishes this session under"},
	{Name: "PARTYLINE_TELEMETRY", Class: Client, Why: "0 opts this machine out of CLI telemetry"},
	{Name: "PARTYLINE_THREAD_ID", Class: Client, Why: "the context thread a session is attached to"},
	{Name: "PARTYLINE_VISUAL", Class: Client, Why: "visual preset for crank output"},
	{Name: "PATH", Class: Client, Why: "inherited when installing the daemon/tray as a service"},
	{Name: "PTLN_BIN", Class: Client, Why: "the ptln binary the tray shells out to"},
	{Name: "PTLN_DEBUG", Class: Client, Why: "session-engine debug logging"},
	{Name: "PTLN_MUX_DEBUG", Class: Client, Why: "mux debug logging"},
	{Name: "PTLN_POWERLINE", Class: Client, Why: "powerline glyphs in the mux status bar"},
	{Name: "SHELL", Class: Client, Why: "the shell a shared session hosts"},
	{Name: "TERM_PROGRAM", Class: Client, Why: "terminal detection"},
	{Name: "TMUX", Class: Client, Why: "terminal detection"},
	{Name: "USER", Class: Client, Why: "fallback display name"},
	{Name: "WEZTERM_PANE", Class: Client, Why: "terminal detection"},
}

// Classify returns the classification for a variable, if it has one.
func Classify(name string) (NonFeature, bool) { return classifyIn(NonFeatures, name) }

func classifyIn(table []NonFeature, name string) (NonFeature, bool) {
	for _, n := range table {
		if n.Name == name {
			return n, true
		}
	}
	return NonFeature{}, false
}

// OfClass returns every classified variable in one class, sorted by name.
func OfClass(c Class) []NonFeature {
	var out []NonFeature
	for _, n := range NonFeatures {
		if n.Class == c {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
