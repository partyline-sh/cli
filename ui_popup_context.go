package main

import (
	"os"
	"regexp"
	"strings"
)

var uiPaneID = regexp.MustCompile(`^%[0-9]+$`)

// TMUX_PANE in display-popup can be inherited from the server environment,
// rather than name the covered pane. Only Partyline popup children receive this
// explicit opening context; agents and normal pane launches keep their own env.
func popupUIEnvironment(args []string) []string {
	if len(args) == 0 || args[0] != "display-popup" {
		return args
	}
	query := []string{"display-message", "-p"}
	if client := popupClient(args[1:]); client != "" {
		query = append(query, "-c", client)
	}
	query = append(query, "#{pane_id}")
	b, err := tmuxCmd(query...).Output()
	pane := strings.TrimSpace(string(b))
	if err != nil || !uiPaneID.MatchString(pane) {
		return args
	}
	out := []string{"display-popup", "-e", "PARTYLINE_UI_PANE=" + pane}
	return append(out, args[1:]...)
}

// Stop at the child command: a child's own -c is not a tmux client selector.
func popupClient(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case "-b", "-d", "-e", "-h", "-s", "-S", "-t", "-T", "-w", "-x", "-y":
			i++
		case "-B", "-C", "-E", "-EE":
		default:
			return ""
		}
	}
	return ""
}

func uiPaneTarget() string {
	if pane := strings.TrimSpace(os.Getenv("PARTYLINE_UI_PANE")); pane != "" {
		return pane
	}
	return strings.TrimSpace(os.Getenv("TMUX_PANE"))
}
