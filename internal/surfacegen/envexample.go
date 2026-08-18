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
	var b bytes.Buffer
	b.WriteString(header("#"))
	b.WriteString(`#
# /opt/partyline/.env on a partyline box. Copy it there, fill in real values, chmod 600.
# Never commit the filled version. CI upserts a subset of these on every deploy (the
# "Sync CI-managed runtime env" step in .github/workflows/deploy-*.yml); everything else is
# hand-set on the box once, by scripts/env-bootstrap.sh and scripts/staging-secrets.sh.
#
# RULE: staging must never reach a customer. Every outbound integration below either gets a
# staging-specific credential or is left EMPTY so the feature stays dark. Do not paste prod values.
#
# The values shown are STAGING examples. Generate every secret with: openssl rand -base64 32
#
# ` + "`ptln server doctor`" + ` reads the same registry this file is generated from and reports each
# feature configured / not-configured, naming the variables a not-configured one is missing.
`)

	section(&b, "Core — the app does not run correctly without these", features.OfClass(features.Core))
	section(&b, "Read by docker compose and the init scripts, not by application code", features.OfClass(features.Compose))

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

	section(&b, "Optional — a supported unset state with a documented default", features.OfClass(features.Optional))
	return b.Bytes()
}

func section(b *bytes.Buffer, title string, vars []features.NonFeature) {
	if len(vars) == 0 {
		return
	}
	fmt.Fprintf(b, "\n# ─── %s %s\n", title, strings.Repeat("─", max(3, 86-len(title))))
	for _, v := range vars {
		// Reuse the shared reflow so a reworded reason changes only its own lines in the diff.
		fmt.Fprintf(b, "# %s\n", wrapComment(v.Name+" — "+v.Why, "#   "))
		fmt.Fprintf(b, "%s=%s\n", v.Name, v.Example)
	}
}
