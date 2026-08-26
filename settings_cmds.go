package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln me` and `ptln notify` — the personal half of "every setting has a CLI path" (#829).
//
// settingsClient is shared by every command in this family. It fails with the fix rather than the
// diagnosis: "not logged in" is useless without the command that fixes it.

func settingsClient() *api.Client {
	c := api.New()
	if c == nil {
		fatal(fmt.Errorf("not logged in on this machine — run: ptln login"))
	}
	return c
}

func apiBase() string { return api.Base() }

// ── ptln me ──────────────────────────────────────────────────────────────────────────────────────

func meCmd(args []string) {
	if len(args) == 0 || args[0] == "show" {
		meShow(args)
		return
	}
	switch args[0] {
	case "help", "-h", "--help":
		meHelp()
	case "set":
		meSet(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ptln me: unknown command %q\n\n", args[0])
		meHelp()
		os.Exit(2)
	}
}

func meHelp() {
	fmt.Print(`Usage: ptln me [show|set]

Your profile.

  show                          who you are (--json for machine output)
  set --name "Ada Lovelace"     your display name
      --handle ada              your @handle (unique across partyline)
      --timezone Europe/London  used for quiet hours and scheduling
      --email you@example.com   where notifications go, if not your login address
      --github ada              your GitHub username

Only the flags you pass are changed; everything else is left alone.
`)
}

func meShow(args []string) {
	p, err := settingsClient().Me()
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(b))
		return
	}
	// An unset field prints as the command that sets it, rather than as blank space — a profile
	// that renders "  @" tells you nothing about what to do next.
	unset := func(v, fix string) string {
		if strings.TrimSpace(v) == "" {
			return "(not set — " + fix + ")"
		}
		return v
	}
	fmt.Printf("name      %s\n", unset(p.DisplayName, "ptln me set --name \"Your Name\""))
	fmt.Printf("handle    %s\n", unset(p.Handle, "ptln me set --handle you"))
	fmt.Printf("email     %s\n", p.Email)
	fmt.Printf("github    %s\n", unset(p.GithubUsername, "ptln me set --github you"))
	fmt.Printf("timezone  %s\n", unset(p.Timezone, "ptln me set --timezone Europe/London"))
}

func meSet(args []string) {
	patch := map[string]any{}
	for flag, field := range map[string]string{
		"--name":     "display_name",
		"--handle":   "handle",
		"--timezone": "timezone",
		"--email":    "notify_email",
		"--github":   "github_username",
	} {
		if v := flagVal(args, flag); v != "" {
			patch[field] = v
		}
	}
	if len(patch) == 0 {
		fatal(fmt.Errorf("nothing to change — see: ptln me help"))
	}
	if err := settingsClient().UpdateMe(patch); err != nil {
		fatal(err)
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("Updated %s.\n", strings.Join(keys, ", "))
}

// ── ptln notify ──────────────────────────────────────────────────────────────────────────────────

func notifyCmd(args []string) {
	if len(args) == 0 || args[0] == "ls" || args[0] == "list" || args[0] == "show" {
		notifyShow(args)
		return
	}
	switch args[0] {
	case "help", "-h", "--help":
		notifyHelp()
	case "on":
		notifySet(args[1:], true)
	case "off":
		notifySet(args[1:], false)
	case "quiet":
		notifyQuiet(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ptln notify: unknown command %q\n\n", args[0])
		notifyHelp()
		os.Exit(2)
	}
}

func notifyHelp() {
	fmt.Print(`Usage: ptln notify [ls|on|off|quiet]

What partyline tells you about, and where.

  ls                       your current settings (--json for machine output)
  on  <event> <channel>    turn one on,  e.g. ptln notify on  work slack
  off <event> <channel>    turn one off, e.g. ptln notify off work email
  quiet <start> <end>      quiet hours,  e.g. ptln notify quiet 22:00 07:00
  quiet off                no quiet hours

Some events have a mandatory email floor (a session invite you cannot silently
miss). Those show as "locked" and refuse to be turned off.
`)
}

func notifyShow(args []string) {
	p, err := settingsClient().NotifyPrefs()
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(b))
		return
	}
	events := make([]string, 0, len(p.Prefs))
	for k := range p.Prefs {
		events = append(events, k)
	}
	sort.Strings(events)
	for _, e := range events {
		chans := make([]string, 0, len(p.Prefs[e]))
		for c := range p.Prefs[e] {
			chans = append(chans, c)
		}
		sort.Strings(chans)
		parts := make([]string, 0, len(chans))
		for _, c := range chans {
			mark := "off"
			if p.Prefs[e][c] {
				mark = "on"
			}
			parts = append(parts, fmt.Sprintf("%s %s", c, mark))
		}
		fmt.Printf("%-18s %s\n", e, strings.Join(parts, "   "))
	}
	if p.QuietStart != "" || p.QuietEnd != "" {
		fmt.Printf("\nquiet hours        %s – %s\n", p.QuietStart, p.QuietEnd)
	}
	if !p.SlackConnected {
		// Turning a slack channel on with no Slack connected is a setting that silently does
		// nothing; say so rather than letting someone think it is configured.
		fmt.Println("\nSlack is not connected — slack channels stay silent until it is.")
	}
}

func notifySet(args []string, on bool) {
	if len(args) < 2 {
		fatal(fmt.Errorf("needs an event and a channel: ptln notify on work slack  (see: ptln notify ls)"))
	}
	event, channel := args[0], args[1]
	p, err := settingsClient().NotifyPrefs()
	if err != nil {
		fatal(err)
	}
	if _, ok := p.Prefs[event]; !ok {
		known := make([]string, 0, len(p.Prefs))
		for k := range p.Prefs {
			known = append(known, k)
		}
		sort.Strings(known)
		fatal(fmt.Errorf("no event called %q. Available: %s", event, strings.Join(known, ", ")))
	}
	if _, ok := p.Prefs[event][channel]; !ok {
		fatal(fmt.Errorf("event %q has no %q channel", event, channel))
	}
	if err := settingsClient().SetNotifyPref(event, channel, on); err != nil {
		fatal(err)
	}
	// The server refuses to turn off a mandatory floor, and silently keeps it on. Read back rather
	// than reporting what we asked for — otherwise the CLI cheerfully claims a change that did not
	// happen.
	after, err := settingsClient().NotifyPrefs()
	if err == nil && after.Prefs[event][channel] != on {
		fmt.Printf("%s/%s stays on — it is a mandatory notification.\n", event, channel)
		return
	}
	state := "off"
	if on {
		state = "on"
	}
	fmt.Printf("%s/%s is now %s.\n", event, channel, state)
}

func notifyQuiet(args []string) {
	if len(args) == 1 && (args[0] == "off" || args[0] == "none") {
		if err := settingsClient().SetQuietHours("", ""); err != nil {
			fatal(err)
		}
		fmt.Println("Quiet hours off.")
		return
	}
	if len(args) < 2 {
		fatal(fmt.Errorf("needs a start and an end: ptln notify quiet 22:00 07:00  (or: ptln notify quiet off)"))
	}
	if err := settingsClient().SetQuietHours(args[0], args[1]); err != nil {
		fatal(err)
	}
	fmt.Printf("Quiet hours %s – %s.\n", args[0], args[1])
}
