package surfacegen

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/features"
)

// The self-host setup section of web/public/llms-full.txt.
//
// WHY IT IS A URL AND NOT A PRODUCT FEATURE (epic H.8). A self-hoster has not installed anything
// yet. Setup instructions therefore cannot live behind `ptln`, and cannot be an MCP prompt in
// cg-mcp — both require the binary installed and logged in, which is exactly what the reader
// lacks. Someone pastes https://partyline.sh/llms-full.txt into any model and gets walked through
// a box. That is the whole delivery mechanism.
//
// WHY IT IS GENERATED. A hand-written setup doc is a doc that goes stale, which is the failure this
// closes: it would keep naming a variable nothing reads, or miss the one that gates the integration
// the reader came for. So the feature list, the variable names, and the compose service list are
// all read from source — features.json and deploy/stack/docker-compose.yml — and `make
// surface-check` fails if the file on disk disagrees.
//
// The prose that a generator cannot derive — what a feature is FOR and where its credential comes
// from — lives in featureNotes below, next to the list it annotates, in the same split llms.go
// already uses. It is keyed by features.json key and checked in BOTH directions at generation
// time: a feature declared with no note fails generation, and a note for a key that is not declared
// fails generation. Neither can drift into being quietly wrong.
//
// NEVER A VALUE. Only variable names and where to go to obtain the credential. No example secret,
// no real credential, nothing read from any live environment.

// featureNote is the human half of one feature's entry: what it does, and where its credential
// comes from. Both are prose; everything else about the feature comes from the registry.
type featureNote struct {
	Does  string // what turning it on gets a self-hoster
	Where string // where to obtain the credential — a place, never a value
}

// featureNotes is keyed by features.json key. Adding a feature to the registry without adding a
// note here is a generation error, on purpose: the doc's job is to say where the credential comes
// from, and an entry that cannot say that is an entry that strands the reader.
var featureNotes = map[string]featureNote{
	"admin_console": {
		Does:  "Unlocks the operator console at /admin for the listed email addresses.",
		Where: "your own account's email address — a comma-separated allowlist, not a credential.",
	},
	"billing": {
		Does:  "Charges for seats through Stripe. A self-hosted box does not need it; left unset, billing stays dark and nothing is metered.",
		Where: "Stripe Dashboard → Developers → API keys for the secret key, Webhooks for the signing secret, and Products for the two price IDs.",
	},
	"discord": {
		Does:  "Lets a Discord bot start and follow runs from a server channel.",
		Where: "Discord Developer Portal → Applications → your app: Bot → Token, and General Information → Public Key.",
	},
	"email": {
		Does:  "Sends transactional email — invites, verification, run notifications. Without it those emails are silently not sent, so set it before inviting anyone.",
		Where: "Resend → API Keys. The from address must be on a domain you have verified in Resend.",
	},
	"github_app": {
		Does:  "Opens pull requests and reads repositories as an app rather than as a person's token.",
		Where: "GitHub → Settings → Developer settings → GitHub Apps → New GitHub App. The app id and slug are on its settings page; generate a private key there and base64-encode the downloaded .pem.",
	},
	"invite_assertions": {
		Does:  "Signs the assertions that let an invited joiner prove an invite is genuine.",
		Where: "generate it yourself: openssl rand -base64 32.",
	},
	"marketing_email": {
		Does:  "Syncs signups to a Loops audience. Purely commercial; a self-hosted box normally leaves it unset.",
		Where: "Loops → Settings → API.",
	},
	"operator_alerts": {
		Does:  "Posts a Slack message when someone signs up.",
		Where: "Slack → your workspace → Incoming Webhooks, which gives you one webhook URL.",
	},
	"redis": {
		Does:  "Backs rate limiting and short-lived coordination state. The compose stack runs a redis service, so on a standard box this points at it.",
		Where: "the redis service in the compose stack — redis://redis:6379. No account anywhere.",
	},
	"relay": {
		Does:  "Registers this box's relay so shared sessions can be joined from outside the LAN. The relay forwards ciphertext it cannot read.",
		Where: "generate both yourself: an id you choose, and openssl rand -base64 32 for the secret. They must match what the relay container is started with.",
	},
	"scribe": {
		Does:  "Distills context threads server-side, so a long thread stays readable without a client doing it.",
		Where: "an API key for the model provider you want the scribe to use — Anthropic by default.",
	},
	"sentry": {
		Does:  "Reports server errors to Sentry. Unset means no error reporting and no outbound telemetry.",
		Where: "Sentry → your project → Settings → Client Keys (DSN).",
	},
	"session_key_wrap": {
		Does:  "Encrypts stored session keys at rest, so a database dump does not hand over live sessions.",
		Where: "generate it yourself: openssl rand -base64 32. Rotating it invalidates existing wrapped keys, so set it before first use.",
	},
	"signup_webhook": {
		Does:  "Posts new signups to an internal Slack channel. Separate from operator_alerts, and normally unset on a self-hosted box.",
		Where: "Slack → Incoming Webhooks.",
	},
	"slack": {
		Does:  "Installs the Slack app so a channel can host a party and drive runs.",
		Where: "api.slack.com/apps → Create New App. Client id and secret are under Basic Information → App Credentials; the signing secret is on the same page.",
	},
	"storage": {
		Does:  "Stores uploads and run artifacts in S3-compatible object storage.",
		Where: "Cloudflare R2 → your bucket → Manage R2 API Tokens for the key pair; the endpoint is https://<account-id>.r2.cloudflarestorage.com. Any S3-compatible provider works.",
	},
	"telegram": {
		Does:  "Lets a Telegram bot start and follow runs from a chat.",
		Where: "Telegram → @BotFather → /newbot for the token; the webhook secret is one you generate and pass to setWebhook.",
	},
	"telemetry": {
		Does:  "Sends product analytics to PostHog. Unset means none are collected.",
		Where: "PostHog → Project Settings → Project API Key.",
	},
	"ticker": {
		Does:  "Authenticates the ticker container's minute-by-minute POST to /api/v1/tick, which reaps stale sessions and resumes rate-limited runs. Set it — without it the sweeps never run.",
		Where: "generate it yourself: openssl rand -base64 32. The same value goes to the web service and the ticker service.",
	},
	"workos": {
		Does:  "Authenticates users. This is the sign-in path for the whole control plane, so a box without it has no way for anyone to log in.",
		Where: "WorkOS Dashboard → API Keys for the secret, and the same page for the client id. Add your box's origin as a redirect URI.",
	},
}

// composeService is one service in deploy/stack/docker-compose.yml, in file order.
type composeService struct {
	Name  string
	Image string
}

var (
	composeServiceRe = regexp.MustCompile(`^  ([a-z][a-z0-9_-]*):\s*$`)
	composeImageRe   = regexp.MustCompile(`^\s+image:\s*(\S+)\s*$`)
)

// readComposeServices extracts the service list from the real compose file, rather than restating
// it. The count is the fact most likely to be wrong in a hand-written doc — the stack has gained
// and lost services — and reading it here means the doc cannot claim a service the box does not
// run, or miss one it does.
func readComposeServices(root string) ([]composeService, error) {
	path := filepath.Join(root, filepath.FromSlash(composePath))
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("self-host setup section: %w", err)
	}
	defer f.Close()

	var out []composeService
	inServices := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		// A line at column zero ends whatever top-level block we were in.
		if !strings.HasPrefix(line, " ") {
			inServices = strings.HasPrefix(line, "services:")
			continue
		}
		if !inServices {
			continue
		}
		if m := composeServiceRe.FindStringSubmatch(line); m != nil {
			out = append(out, composeService{Name: m[1]})
			continue
		}
		if m := composeImageRe.FindStringSubmatch(line); m != nil && len(out) > 0 && out[len(out)-1].Image == "" {
			out[len(out)-1].Image = m[1]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("self-host setup section: reading %s: %w", composePath, err)
	}
	// A silently empty service list would publish a setup doc that describes no stack at all.
	// Refuse to generate instead — the parse having stopped matching is the likelier cause.
	if len(out) < 2 {
		return nil, fmt.Errorf("self-host setup section: found %d services in %s — the parse is wrong, not the stack", len(out), composePath)
	}
	for _, s := range out {
		if s.Image == "" {
			return nil, fmt.Errorf("self-host setup section: service %q in %s has no image", s.Name, composePath)
		}
	}
	return out, nil
}

const (
	composePath    = "deploy/stack/docker-compose.yml"
	migrationsPath = "deploy/stack/apply-migrations.sh"
)

// genSelfHost writes the setup section. Returns an error rather than a partial section: a setup
// doc missing the feature the reader came for is worse than a build that stopped.
func genSelfHost(root string, reg features.Registry) ([]byte, error) {
	svcs, err := readComposeServices(root)
	if err != nil {
		return nil, err
	}
	if err := checkNotesCoverRegistry(reg); err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteString("## Self-hosting partyline\n\n")
	b.WriteString(`partyline runs on one box you own. There is no supported hosted-plus-agent split: the control
plane is a docker compose stack, and the agents were always running on your machines anyway. This
section is generated from the stack definition and the feature registry in the repository, so the
service list and the variable names below are the ones the code actually uses.

`)

	b.WriteString("### The stack\n\n")
	fmt.Fprintf(&b, "The control plane is a %d-service compose stack (`%s`):\n\n", len(svcs), composePath)
	for _, s := range svcs {
		fmt.Fprintf(&b, "- `%s` — `%s`\n", s.Name, s.Image)
	}
	b.WriteString("\npartyline's own images are published on `ghcr.io/partyline-sh/` and are pulled, not built:\n")
	b.WriteString("nothing in the stack compiles on your box. Everything else is an upstream image.\n\n")
	fmt.Fprintf(&b, "Database migrations are applied BY THE DEPLOY, not by hand: `%s`\n", migrationsPath)
	b.WriteString("runs plain `psql` against the box's own Postgres before the containers are swapped. There is no\n")
	b.WriteString("migration CLI to install and no `db push` step — do not apply SQL yourself.\n\n")
	b.WriteString("Configuration is one file on the box, `/opt/partyline/.env`, mode 600. `deploy/stack/env.example`\n")
	b.WriteString("lists every variable; `scripts/env-bootstrap.sh` generates the derived secrets. After the box is\n")
	b.WriteString("up, `ptln server doctor` reports each feature below as configured or not, naming the variables a\n")
	b.WriteString("not-configured one is missing.\n\n")

	b.WriteString("### Features and the variables that turn them on\n\n")
	b.WriteString("A feature is CONFIGURED when EVERY variable in its block is set, and NOT CONFIGURED otherwise.\n")
	b.WriteString("Two states, no middle: leaving a block empty is a supported choice and that feature stays dark.\n")
	b.WriteString("Set only what you need — none of the blocks below is required for the stack to start.\n\n")
	for _, f := range reg.Features {
		n := featureNotes[f.Key]
		fmt.Fprintf(&b, "#### %s (`%s`)\n\n", f.Label, f.Key)
		fmt.Fprintf(&b, "%s\n\n", n.Does)
		fmt.Fprintf(&b, "- Variables: `%s`\n", strings.Join(f.Env, "`, `"))
		fmt.Fprintf(&b, "- Where to get it: %s\n", n.Where)
		fmt.Fprintf(&b, "- Reference: `%s`\n\n", f.Docs)
	}
	b.WriteString("Never paste a value from someone else's box. Every secret above is either generated on your own\n")
	b.WriteString("machine or issued to you by the provider named next to it.\n")
	return b.Bytes(), nil
}

// checkNotesCoverRegistry enforces the registry and the prose in both directions. A feature with no
// note would render an empty entry; a note for a key nobody declares would document something that
// does not exist. Both are generation errors, so neither can be discovered by a reader instead.
func checkNotesCoverRegistry(reg features.Registry) error {
	var missing, extra []string
	declared := map[string]bool{}
	for _, f := range reg.Features {
		declared[f.Key] = true
		n, ok := featureNotes[f.Key]
		if !ok || strings.TrimSpace(n.Does) == "" || strings.TrimSpace(n.Where) == "" {
			missing = append(missing, f.Key)
		}
	}
	for k := range featureNotes {
		if !declared[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		return fmt.Errorf("self-host setup section: %s declared in features.json with no entry in surfacegen.featureNotes — add what it does and where its credential comes from", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("self-host setup section: surfacegen.featureNotes documents %s, which features.json does not declare — if it is worth documenting it is worth declaring", strings.Join(extra, ", "))
	}
	return nil
}
