package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"partyline.sh/partyline/internal/api"
)

// setup_read / setup_write — configuring a self-hosted instance from an agent, over the same one
// endpoint a person uses.
//
// WHY THESE EXIST. partyline is self-host-only, which means every instance has an operator, and the
// operator is increasingly an agent. Instance configuration is drivable three ways — a first-run
// wizard, the settings page, and these tools — and all three call /api/v1/setup, so none of them can
// hold an opinion the others don't. Without these, "what does this instance still need?" is only
// answerable in a logged-in browser, which is exactly the answer an agent cannot get.
//
// Security invariants:
//  1. REFERENCE, NOT COMMAND. Both tools send DATA to one fixed path (api.Client.Setup /
//     UpdateSetup). No argument here reaches a path, an argv, or a shell string; `instance_name`
//     travels as a JSON string field and nothing else. There is deliberately no generic "call the
//     API" tool for a model to steer.
//  2. The write takes exactly two typed, optional fields, validated locally before the request and
//     again server-side. An unknown key in `arguments` is dropped by the decoder, not forwarded.
//  3. Authorization is the SERVER's: PATCH is instance-admin only and answers 403 otherwise. The
//     tool's job is to make that refusal actionable, never to guess at it or work around it.
//  4. No secret is printed. The settings this reports are a name and a boolean; the steps' detail
//     text names environment VARIABLES (OIDC_ISSUER) but never their values, and free text still
//     goes through redactSecrets on the way out.

// cgSetupToolDefs is appended to cgToolDefs (see cg_mcp.go) so tools/list advertises both.
var cgSetupToolDefs = []map[string]any{
	{
		"name": "setup_read",
		"description": "Read THIS partyline instance's setup state — every setup step with its state (done / todo / " +
			"blocked) and what to do about it, which step is next, and whether the instance is set up at all. Use " +
			"this to answer \"what does this instance still need?\" without a browser, before telling anyone the " +
			"install is finished, or when something that should work does not (no machine connected and no project " +
			"registered are setup facts, not bugs). Completion is recomputed from the world on every call, never " +
			"read from a \"finished\" flag, so a step that stopped being true says so. Read-only. Takes no arguments.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "setup_write",
		"description": "Change what this partyline instance lets the app change about itself: `instance_name` (what " +
			"this deployment is called — worth setting when someone runs more than one) and `allow_signups` " +
			"(whether new people may create an account here). Pass either or both; an omitted field is left alone. " +
			"INSTANCE-ADMIN ONLY — the first account to sign in owns the instance, and anyone else gets a refusal " +
			"explaining that. Everything else about an instance (identity provider, database, domain) is environment " +
			"set before the container starts and CANNOT be changed here — call setup_read to see which of those are " +
			"still outstanding.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"instance_name": map[string]any{
					"type":        "string",
					"description": "what to call this deployment, 1–60 characters (e.g. 'acme staging').",
				},
				"allow_signups": map[string]any{
					"type":        "boolean",
					"description": "true lets new people create an account on this instance; false closes it.",
				},
			},
		},
	},
}

const setupMaxNameLen = 60

// handleSetupRead → this instance's setup state as compact, actionable text.
func (s *cgServer) handleSetupRead(enc *json.Encoder, req rpcReq) {
	// Deliberately NOT gated on a local token. The GET is readable without signing in while — and
	// only while — no account exists, which is the whole point of a first-run wizard: the first step
	// is "nobody can sign in yet", and requiring a signed-in caller to read it means it can only be
	// seen in a state where it is already done. Once accounts exist the server answers 401 and the
	// message below carries the fix.
	st, err := s.c.Setup()
	if err != nil {
		s.toolResult(enc, req.ID, setupErrorMessage("setup_read", err), true)
		return
	}
	s.toolResult(enc, req.ID, formatSetupState(st), false)
}

// handleSetupWrite → PATCH the two settings the app owns, or an actionable refusal.
func (s *cgServer) handleSetupWrite(enc *json.Encoder, req rpcReq) {
	var p struct {
		Args struct {
			// Pointers: an ABSENT field must be distinguishable from a zero one. `allow_signups:
			// false` is a real instruction, and a plain bool would make it indistinguishable from
			// "not mentioned" — which is how a tool call quietly closes signups nobody asked to close.
			InstanceName *string `json:"instance_name"`
			AllowSignups *bool   `json:"allow_signups"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)

	name, allow := p.Args.InstanceName, p.Args.AllowSignups
	if name == nil && allow == nil {
		s.toolResult(enc, req.ID, "This tool needs something to change: `instance_name` (1–60 characters) "+
			"and/or `allow_signups` (true/false). Call setup_read first if you want to see what they are now.", true)
		return
	}
	if name != nil {
		trimmed, errText := validSetupName(*name)
		if errText != "" {
			s.toolResult(enc, req.ID, errText, true)
			return
		}
		name = &trimmed
	}
	if api.LoadToken() == "" {
		// A write, unlike the read, always needs an account: the endpoint's virgin-instance exception
		// is read-only. Fail closed rather than sending a request that cannot succeed.
		s.toolResult(enc, req.ID, notSignedIn, true)
		return
	}
	out, err := s.c.UpdateSetup(name, allow)
	if err != nil {
		s.toolResult(enc, req.ID, setupErrorMessage("setup_write", err), true)
		return
	}
	s.toolResult(enc, req.ID, formatSetupSettings(out), false)
}

// validSetupName applies the endpoint's own rule locally, so an obvious mistake costs no round trip
// and comes back naming the actual limit. Control characters are rejected here and not there: a name
// is rendered into a wizard, a page title and this tool's own output, and a newline in it would let
// a "setting" restructure text a model reads.
func validSetupName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "`instance_name` cannot be empty — give this deployment a name, 1–60 characters."
	}
	if n := len([]rune(name)); n > setupMaxNameLen {
		return "", fmt.Sprintf("`instance_name` must be 1–%d characters; that one is %d.", setupMaxNameLen, n)
	}
	for _, r := range name {
		if r != ' ' && unicode.IsControl(r) {
			return "", "`instance_name` must be a single line of plain text — no line breaks or control characters."
		}
	}
	return name, ""
}

// setupErrorMessage turns a transport/authorization failure into the sentence that names the fix.
// The 403 is the one that matters: "403" alone reads as a bug, when it is a fact about WHO is asking.
func setupErrorMessage(tool string, err error) string {
	switch {
	case err == api.ErrSetupForbidden:
		return "Refused: only an administrator of this instance can change its settings. On a self-hosted " +
			"partyline the FIRST account to sign in owns the instance — you are signed in as someone else. " +
			"Ask that person to make the change (Settings → Instance, or setup_write from their own session). " +
			"Nothing was changed. `setup_read` still works for you and needs no admin rights."
	case err == api.ErrSetupNotSignedIn:
		return notSignedIn + " (This instance already has accounts, so its setup is no longer readable " +
			"anonymously.)"
	case err == api.ErrSetupNothingToChange:
		return "Nothing to change — pass `instance_name` and/or `allow_signups`."
	default:
		return tool + ": " + redactSecrets(err.Error())
	}
}

// formatSetupState renders the state so the NEXT ACTION is the thing you cannot miss. A checklist a
// model has to parse into an action is a checklist it will summarize instead of acting on.
func formatSetupState(st *api.SetupState) string {
	var b strings.Builder
	name := strings.TrimSpace(st.Settings.InstanceName)
	if name == "" {
		name = "(unnamed)"
	}
	fmt.Fprintf(&b, "instance: %s\n", redactSecrets(name))
	fmt.Fprintf(&b, "signups: %s\n", map[bool]string{true: "open — anyone may create an account here",
		false: "closed — no new accounts"}[st.Settings.AllowSignups])

	done := 0
	for _, s := range st.Steps {
		if s.State == "done" {
			done++
		}
	}
	if st.Complete {
		fmt.Fprintf(&b, "setup: COMPLETE (%d/%d steps)\n", done, len(st.Steps))
	} else {
		fmt.Fprintf(&b, "setup: INCOMPLETE (%d/%d steps done)\n", done, len(st.Steps))
	}

	if st.Next != nil {
		fmt.Fprintf(&b, "\nNEXT: %s (%s)\n", redactSecrets(st.Next.Title), st.Next.ID)
		if d := strings.TrimSpace(st.Next.Detail); d != "" {
			fmt.Fprintf(&b, "  %s\n", redactSecrets(d))
		}
		if st.Next.EnvOnly {
			b.WriteString("  This one is not an API call: a person must edit .env and restart the instance.\n")
		}
	} else if !st.Complete {
		// next==null with steps outstanding means everything left is BLOCKED — worth saying, because
		// "nothing to do next" and "nothing you can do yet" are opposite instructions.
		b.WriteString("\nNEXT: nothing actionable — every remaining step is blocked by an earlier one (see below).\n")
	}

	b.WriteString("\nsteps:\n")
	for _, s := range st.Steps {
		fmt.Fprintf(&b, "  [%-7s] %-9s %s\n", s.State, s.ID, redactSecrets(s.Title))
		if d := strings.TrimSpace(s.Detail); d != "" {
			fmt.Fprintf(&b, "              %s\n", redactSecrets(d))
		}
		if s.BlockedBy != "" {
			fmt.Fprintf(&b, "              blocked by: %s\n", s.BlockedBy)
		}
		if s.EnvOnly && s.State != "done" {
			b.WriteString("              needs .env + a restart — no API call can finish this step\n")
		}
	}
	if !st.Complete {
		b.WriteString("\nOnly `instance_name` and `allow_signups` are settable from here (setup_write, " +
			"instance-admin only). Every other step is done by a person on the box or in a browser.\n")
	}
	return b.String()
}

// formatSetupSettings confirms what the instance now says — read back from the response rather than
// echoed from the request, so a value the server normalized (or refused to move) is reported as it
// actually stands.
func formatSetupSettings(s *api.SetupSettings) string {
	name := strings.TrimSpace(s.InstanceName)
	if name == "" {
		name = "(unnamed)"
	}
	return fmt.Sprintf("Updated. This instance now reads:\n  instance_name: %s\n  allow_signups: %v\n\n"+
		"Call setup_read for the full step list.", redactSecrets(name), s.AllowSignups)
}
