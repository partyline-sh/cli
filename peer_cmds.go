package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"partyline.sh/partyline/internal/daemonctl"
)

// peer_cmds.go — `ptln peer approve|decline <id>`: the CLI edge the TRAY acts through.
//
// WHY THE TRAY DOESN'T DO THIS ITSELF. ptln-tray holds no account token, opens no socket and makes no
// HTTP call; it shells `ptln` and always has. Keeping it that way means the tray gains no new
// capability from becoming able to approve a consult — the privilege stays where it already lived.
//
// WHY APPROVE STILL CAN'T BE BLIND. daemonctl.ApproveConsult must echo a digest of the question text
// the caller DISPLAYED, and the daemon recomputes it and refuses a mismatch. So this command cannot
// approve an id it hasn't fetched, and there is no flag to make it. It fetches the daemon's own copy,
// PRINTS it, and only then approves — so even invoked straight from a shell the question is on screen
// before the answer turn starts. That is the proof-of-surfacing invariant working as designed, not a
// limitation to route around: the tray's submenu shows the same text before the click, and the digest
// is what makes both surfaces honest.

func peerUsage() {
	fmt.Println(`Usage: ptln peer <command>

  approve <consult-id>    answer a teammate's queued question with ONE read-only turn on this
                          machine's checkout. Prints the question first — approving without
                          showing it is not possible (the daemon requires a digest of what
                          was displayed).
  decline <consult-id>    decline it, freeing the asker now instead of at the timeout.

Both go through this machine's local daemon (a 0600 unix socket, no token on the wire); the
daemon re-validates that the question was genuinely addressed to it. To READ your peer messages,
use ctrl-\ p inside ptln.`)
}

func peerMain(args []string) {
	if len(args) == 0 {
		peerUsage()
		return
	}
	switch args[0] {
	case "approve":
		peerApprove(args[1:])
	case "decline", "deny":
		peerDecline(args[1:])
	case "-h", "--help", "help":
		peerUsage()
	default:
		fmt.Fprintf(os.Stderr, "ptln peer: unknown subcommand %q\n", args[0])
		peerUsage()
		os.Exit(2)
	}
}

func peerApprove(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln peer approve <consult-id>"))
	}
	id := args[0]
	ctl := daemonctl.Local()
	q, err := ctl.GetConsult(id)
	if err != nil {
		fatal(fmt.Errorf("%s", peerCtlNote(err)))
	}
	// Display precedes decision, always. The digest below is of exactly these words.
	fmt.Printf("a teammate asks about %q (waiting %s):\n\n%s\n\n", q.Project,
		shortDuration(time.Duration(q.WaitingSec)*time.Second), clipQuestion(q.Question))
	if err := ctl.ApproveConsult(id, q.Question); err != nil {
		fatal(fmt.Errorf("couldn't start the answer: %w", err))
	}
	fmt.Printf("✓ answering %s read-only on this machine — the answer posts back when the turn finishes\n", id)
}

func peerDecline(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln peer decline <consult-id>"))
	}
	id := args[0]
	if err := daemonctl.Local().DenyConsult(id, "declined by owner"); err != nil {
		fatal(fmt.Errorf("%s", peerCtlNote(err)))
	}
	fmt.Printf("✓ declined %s — they're freed now rather than waiting out the window\n", id)
}

// peerCtlNote turns a control-channel failure into advice. "No daemon" is the common one and isn't an
// error in the user's world; everything else (unknown id, already decided, addressed to another
// machine) is the daemon's one deliberately indistinguishable refusal, passed through verbatim.
func peerCtlNote(err error) string {
	if errors.Is(err, daemonctl.ErrNoDaemon) {
		return "no daemon is running here — start `ptln daemon` to answer from this machine"
	}
	return err.Error()
}
