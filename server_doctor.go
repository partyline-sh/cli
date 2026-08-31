package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/features"
	"partyline.sh/partyline/internal/identity"
)

// ptln server doctor — which features this box has configured, and what a not-configured one is
// missing.
//
// The registry is EMBEDDED, because the machine running this is a Hetzner box with a docker image
// on it, not a checkout. features.json is the single declaration; deploy/stack/env.example is
// generated from the same file, so the doctor and the env example cannot disagree.
//
// NEVER PRINT A VALUE. Doctor output is meant to be pasted into an issue or a Slack thread — that
// is the whole point of having it — so it reports variable NAMES and set/unset, and nothing else
// ever touches a value. renderServerDoctor takes a lookup function rather than calling os.Getenv
// itself so a test can hand it sentinel values and assert none of them come out.
//
//go:embed features.json
var featuresJSON []byte

func serverMain(args []string) {
	if len(args) == 0 {
		serverUsage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "doctor":
		serverDoctorMain(args[1:])
	case "bootstrap":
		serverBootstrapMain(args[1:])
	case "install":
		serverInstallMain(args[1:])
	case "upgrade":
		serverDay2Main("upgrade", args[1:])
	case "backup":
		serverDay2Main("backup", args[1:])
	case "status":
		serverDay2Main("status", args[1:])
	case "tunnel":
		dir, err := findInstallDir(os.Stat)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ptln server tunnel: "+err.Error())
			os.Exit(1)
		}
		if !serverTunnel(dir, liveInstallOps()) {
			os.Exit(1)
		}
	case "help", "--help", "-h":
		serverUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "ptln server: unknown subcommand %q\n\n", args[0])
		serverUsage(os.Stderr)
		os.Exit(2)
	}
}

func serverUsage(w io.Writer) {
	spec, _ := clispec.Lookup("server")
	clispec.PrintHelp(w, spec)
}

func serverDoctorMain(args []string) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--help", "-h", "help":
			serverUsage(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "ptln server doctor: unknown flag %q (flags: --json)\n", a)
			os.Exit(2)
		}
	}

	// Validate at startup and FAIL LOUDLY. Degrading to an empty registry would print "no features
	// configured", which reads as a broken box and sends the operator to fix the wrong thing.
	reg, err := features.Parse(featuresJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln server doctor: the embedded feature registry is invalid: %v\n", err)
		fmt.Fprintln(os.Stderr, "This is a bug in this build, not a problem with this machine's configuration.")
		fmt.Fprintln(os.Stderr, "Nothing about the environment could be checked — do NOT read this as \"no features configured\".")
		os.Exit(2)
	}

	if renderServerDoctor(os.Stdout, reg, os.Getenv, asJSON) {
		return
	}
	// Not an error: a box that deliberately leaves an optional integration unset is correctly
	// configured. The exit code stays 0 so this is safe in a deploy script that only cares about a
	// crash.
}

// renderServerDoctor writes the report and reports whether everything is configured.
func renderServerDoctor(w io.Writer, reg features.Registry, look func(string) string, asJSON bool) bool {
	statuses := reg.Status(look)
	all := true
	for _, st := range statuses {
		if !st.Configured {
			all = false
		}
	}

	// The identity trust root, by fingerprint. A self-hosted instance signs join assertions with
	// its OWN key, and a client pins whatever it was handed at `ptln login` — so the only way to
	// tell a legitimate instance from an impersonated one is to compare the two fingerprints, and
	// this is the end that can be read straight off the box (H.2).
	//
	// This is the one line that reads a secret's VALUE, and it is safe: what comes out is a
	// one-way hash of the PUBLIC half. The private key is never printed, and an unusable value
	// reports only that it is unusable — never what it contained.
	trustFP, trustNote := "", ""
	if v := strings.TrimSpace(look("PARTYLINE_ASSERT_KEY")); v == "" {
		trustNote = "not configured — this instance signs no identity assertions"
	} else if pub, err := identity.PublicFromPrivateB64(v); err != nil {
		trustNote = "PARTYLINE_ASSERT_KEY is set but unusable (" + err.Error() + ")"
	} else {
		trustFP = identity.Fingerprint(pub)
	}

	if asJSON {
		type row struct {
			Feature    string   `json:"feature"`
			Label      string   `json:"label"`
			Configured bool     `json:"configured"`
			Missing    []string `json:"missing,omitempty"`
			Docs       string   `json:"docs"`
		}
		rows := make([]row, 0, len(statuses))
		for _, st := range statuses {
			rows = append(rows, row{st.Key, st.Label, st.Configured, st.Missing, st.Docs})
		}
		out := struct {
			Features          []row  `json:"features"`
			TrustRoot         string `json:"identity_trust_root,omitempty"`
			TrustRootUnusable string `json:"identity_trust_root_note,omitempty"`
		}{rows, trustFP, trustNote}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil { // unreachable for these types, but never print a half-written report
			fmt.Fprintln(w, "{}")
			return all
		}
		fmt.Fprintln(w, string(b))
		return all
	}

	fmt.Fprintln(w, "ptln server doctor — feature configuration on this machine")
	fmt.Fprintln(w, "(names and set/unset only; no value is ever printed, so this is safe to paste into an issue)")
	fmt.Fprintln(w)

	width := 0
	for _, st := range statuses {
		width = max(width, len(st.Key))
	}
	for _, st := range statuses {
		if st.Configured {
			fmt.Fprintf(w, "  ✓ %-*s  configured    %s\n", width, st.Key, st.Label)
			continue
		}
		fmt.Fprintf(w, "  ✗ %-*s  NOT configured %s\n", width, st.Key, st.Label)
		fmt.Fprintf(w, "    %*s  missing: %s\n", width, "", strings.Join(st.Missing, ", "))
		fmt.Fprintf(w, "    %*s  next: add %s to /opt/partyline/.env on this box, then `docker compose up -d web` — see %s\n",
			width, "", plural(len(st.Missing), "that variable", "those variables"), st.Docs)
	}

	fmt.Fprintln(w)
	if trustFP != "" {
		fmt.Fprintf(w, "  identity trust root  %s\n", trustFP)
		fmt.Fprintln(w, "    a client pins this at `ptln login <url>` and prints the same fingerprint — they must match")
	} else {
		fmt.Fprintf(w, "  identity trust root  %s\n", trustNote)
	}

	fmt.Fprintln(w)
	if all {
		fmt.Fprintln(w, "Every declared feature is configured.")
	} else {
		fmt.Fprintln(w, "A not-configured feature is a supported state — that integration is simply dark.")
		fmt.Fprintln(w, "Declared in features.json; deploy/stack/env.example is generated from the same file.")
	}
	return all
}
