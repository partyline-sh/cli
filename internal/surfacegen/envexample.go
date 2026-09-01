package surfacegen

import (
	"bytes"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/features"
)

// genEnvExample writes deploy/stack/env.example from the registry.
//
// It used to be hand-maintained, and had drifted exactly the way a hand-maintained restatement
// does: it listed GITHUB_APP_CLIENT_ID, which no code reads, and omitted SLACK_CLIENT_ID /
// _CLIENT_SECRET / _SIGNING_SECRET, which gate the whole Slack app. Generating it means the file
// and `ptln server doctor` answer from the same declaration, so they cannot disagree about what a
// box needs.
//
// Feature blocks come from features.json; everything else comes from the classification table in
// internal/features/classify.go, which exists so that "not a feature gate" is a stated reason
// rather than an omission. Client-class variables are excluded on purpose — they are read by the
// CLI on a user's machine and have no business in a server env file.
func genEnvExample(reg features.Registry) []byte {
	return envExample(reg, ourBoxes)
}

// genEnvReferencePublic is the copy served at /self-host/env.example.
//
// It differs from ours in the only two ways that matter off our infrastructure, and both exist
// because of one trap. Our file says "copy it to .env and fill in real values" and carries STAGING
// example values (SITE_URL=https://staging.partyline.sh, PGRST_URL=http://postgrest:3000,
// WEB_TAG=staging). env-bootstrap.sh is IDEMPOTENT — it adds only what is missing — so a
// self-hoster who followed that instruction would hand the bootstrap a file where every one of
// those already "looks set", and it would leave them exactly as they are. The box then comes up
// pointed at OUR staging hostname, with no error anywhere, and the mistake survives every re-run
// of the thing that is supposed to fix configuration.
//
// So the published copy (a) says plainly that it is a reference and must NOT be copied to .env, and
// (b) carries no values at all. Nothing an outside reader can paste is environment-specific,
// because there is nothing to paste.
func genEnvReferencePublic(reg features.Registry) []byte {
	return envExample(reg, published)
}

type envAudience int

const (
	ourBoxes envAudience = iota
	published
)

func envExample(reg features.Registry, who envAudience) []byte {
	var b bytes.Buffer
	b.WriteString(header("#"))
	if who == ourBoxes {
		b.WriteString(`#
# /opt/partyline/.env on a partyline box. Copy it there, fill in real values, chmod 600.
# Never commit the filled version. CI upserts a subset of these on every deploy (the
# "Sync CI-managed runtime env" step in .github/workflows/deploy-prod.yml); everything else is
# hand-set on the box once by scripts/env-bootstrap.sh, or appended to .env by hand.
#
# RULE: staging must never reach a customer. Every outbound integration below either gets a
# staging-specific credential or is left EMPTY so the feature stays dark. Do not paste prod values.
#
# The values shown are STAGING examples. Generate every secret with: openssl rand -base64 32
#
# ` + "`ptln server doctor`" + ` reads the same registry this file is generated from and reports each
# feature configured / not-configured, naming the variables a not-configured one is missing.
`)
	} else {
		b.WriteString(`#
# EVERY VARIABLE A PARTYLINE BOX READS, AND WHY — a reference, not a template.
#
# DO NOT COPY THIS FILE TO .env. Run env-bootstrap.sh instead
# (https://partyline.sh/self-host/env-bootstrap.sh): it generates the secrets, MINTS the two
# PostgREST JWTs, and derives everything that follows from your hostname.
#
# The reason is worth one sentence, because the failure is silent: env-bootstrap only ever ADDS
# what is missing, so any line copied from here counts as already set and is left alone forever.
# That is why this copy carries NO values — there is deliberately nothing here to paste.
#
# Fill in by hand, after the bootstrap, only the credentials it cannot know: your identity
# provider, your object storage, and whichever feature blocks below you actually want.
#
# ` + "`ptln server doctor`" + ` reads the same registry this file is generated from and reports each
# feature configured / not-configured, naming the variables a not-configured one is missing.
#
# Full guide: https://partyline.sh/docs/self-host
`)
	}

	section(&b, who, "Core — the app does not run correctly without these", features.OfClass(features.Core))
	section(&b, who, "Read by docker compose and the init scripts, not by application code", features.OfClass(features.Compose))

	fmt.Fprintf(&b, "\n# %s\n", strings.Repeat("═", 90))
	b.WriteString(`# FEATURES — one block per entry in features.json.
#
# A feature is CONFIGURED when every variable in its block is set, and NOT CONFIGURED otherwise.
# Two states, no middle: leaving a block empty is a supported choice and the feature stays dark.
`)
	for _, f := range reg.Features {
		fmt.Fprintf(&b, "\n# ─── [%s] %s ", f.Key, f.Label)
		fmt.Fprintln(&b, strings.Repeat("─", max(3, 84-len(f.Key)-len(f.Label))))
		for _, v := range f.Env {
			fmt.Fprintf(&b, "%s=\n", v)
		}
	}

	section(&b, who, "Optional — a supported unset state with a documented default", features.OfClass(features.Optional))
	return b.Bytes()
}

func section(b *bytes.Buffer, who envAudience, title string, vars []features.NonFeature) {
	if len(vars) == 0 {
		return
	}
	fmt.Fprintf(b, "\n# ─── %s %s\n", title, strings.Repeat("─", max(3, 86-len(title))))
	for _, v := range vars {
		// Reuse the shared reflow so a reworded reason changes only its own lines in the diff.
		fmt.Fprintf(b, "# %s\n", wrapComment(v.Name+" — "+v.Why, "#   "))
		if who == published {
			// No example value, ever: an Example here is one of OUR hostnames or tags, and the
			// published file must contain nothing a reader can usefully copy.
			fmt.Fprintf(b, "%s=\n", v.Name)
			continue
		}
		fmt.Fprintf(b, "%s=%s\n", v.Name, v.Example)
	}
}
