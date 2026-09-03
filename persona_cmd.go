package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// persona_cmd.go — `ptln persona`: change how the agents behave, from the terminal.
//
// The planning agent's own text is the highest-leverage thing in a product whose thesis is that
// better planning yields better software, and until now it was the hardest thing to touch: a
// TypeScript constant, so altering it meant a code change and a deploy.
//
// The loop this gives you is `ptln persona edit plan` → your editor opens on the live text → save →
// the next planning session uses it. Every save is a version, `history` reads them back, and
// `revert` is a pointer move. That is the whole command.

func personaUsage() {
	fmt.Println(`Usage: ptln persona ls                    every agent persona, and which you have edited
       ptln persona show <key>            print the live text
       ptln persona edit <key>            open it in $EDITOR; saving publishes a new version
       ptln persona history <key>         every saved version, newest first
       ptln persona revert <key> [<n>]    go back to version n — or omit n for the shipped default

Keys are the agent modes: plan, describe, fix, approach, prd, incident, brainstorm,
project_setup, chat.

  edit --note "<why>"    record why you changed it — history is much more use with it`)
}

func personaMain(args []string) {
	if len(args) == 0 {
		personaUsage()
		return
	}
	sub := args[0]
	rest := args[1:]
	key := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		key = strings.TrimSpace(rest[0])
	}

	switch sub {
	case "-h", "--help", "help":
		personaUsage()
	case "ls", "list":
		personaList()
	case "show":
		personaShow(mustPersonaKey(key))
	case "edit":
		personaEdit(mustPersonaKey(key), rest)
	case "history", "log", "versions":
		personaHistoryCmd(mustPersonaKey(key))
	case "revert", "rollback":
		personaRevert(mustPersonaKey(key), rest)
	default:
		fmt.Fprintf(os.Stderr, "ptln persona: unknown subcommand %q\n", sub)
		personaUsage()
		os.Exit(2)
	}
}

func mustPersonaKey(k string) string {
	if k == "" {
		fatal(fmt.Errorf("ptln persona: which one? try `ptln persona ls`"))
	}
	return k
}

func personaClient() *api.Client {
	if api.Unconfigured() || api.LoadToken() == "" {
		fatal(fmt.Errorf("not signed in — run `ptln login` first"))
	}
	return api.New()
}

func personaList() {
	ps, err := personaClient().Personas()
	if err != nil {
		fatal(fmt.Errorf("ptln persona ls: %w", err))
	}
	fmt.Printf("\n☎ agent personas at %s\n\n", api.Base())
	for _, p := range ps {
		mark := "  "
		state := "shipped default"
		if p.Edited {
			// The distinction that matters: an unedited persona keeps tracking releases, an edited
			// one is pinned to your text and will not move when you update.
			mark = "✎ "
			state = fmt.Sprintf("yours, v%d", p.Version)
		}
		fmt.Printf("  %s%-15s %-34s %s\n", mark, p.Key, truncateOneLine(p.Name, 34), state)
	}
	fmt.Printf("\n  ✎ = you have edited it (pinned to your text; shipped updates no longer apply)\n")
	fmt.Printf("  edit one with `ptln persona edit <key>`\n\n")
}

func personaShow(key string) {
	p, err := personaClient().Persona2(key)
	if err != nil {
		fatal(fmt.Errorf("ptln persona show %s: %w", key, err))
	}
	src := "shipped default"
	if p.Edited {
		src = fmt.Sprintf("your version %d", p.Version)
	}
	fmt.Printf("\n☎ %s — %s (%s)\n\n", p.Key, p.Name, src)
	fmt.Println(p.Preamble)
	fmt.Println()
}

func personaEdit(key string, args []string) {
	note := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--note" && i+1 < len(args) {
			note = args[i+1]
		}
	}
	c := personaClient()
	p, err := c.Persona2(key)
	if err != nil {
		fatal(fmt.Errorf("ptln persona edit %s: %w", key, err))
	}

	edited, err := editInEditor(key, p.Preamble)
	if err != nil {
		fatal(fmt.Errorf("ptln persona edit %s: %w", key, err))
	}
	if strings.TrimSpace(edited) == strings.TrimSpace(p.Preamble) {
		fmt.Println("  · unchanged — nothing saved")
		return
	}
	if strings.TrimSpace(edited) == "" {
		// An empty persona would silently un-persona the agent, which looks like a model
		// regression rather than an edit. Refused here as well as server-side.
		fatal(fmt.Errorf("ptln persona edit %s: an empty persona would disable the agent — aborted, nothing saved", key))
	}
	v, err := c.SavePersona(key, edited, note)
	if err != nil {
		fatal(fmt.Errorf("ptln persona edit %s: %w", key, err))
	}
	fmt.Printf("  ✓ saved %s as version %d — every new %s session uses it now\n", key, v, key)
	fmt.Printf("    undo with `ptln persona revert %s`\n", key)
}

func personaHistoryCmd(key string) {
	p, err := personaClient().PersonaHistory(key)
	if err != nil {
		fatal(fmt.Errorf("ptln persona history %s: %w", key, err))
	}
	if len(p.History) == 0 {
		fmt.Printf("\n  %s has never been edited here — it is the shipped default.\n\n", key)
		return
	}
	fmt.Printf("\n☎ %s — %d saved version(s)\n\n", key, len(p.History))
	for _, h := range p.History {
		live := " "
		if p.Version != nil && *p.Version == h.Version {
			live = "▸"
		}
		note := h.Note
		if note == "" {
			note = "(no note)"
		}
		fmt.Printf("  %s v%-4d %s  %s\n", live, h.Version, h.CreatedAt[:10], truncateOneLine(note, 52))
	}
	fmt.Printf("\n  ▸ = live.  `ptln persona revert %s <n>` to go back.\n\n", key)
}

func personaRevert(key string, args []string) {
	version := 0
	for _, a := range args[min(1, len(args)):] {
		if !strings.HasPrefix(a, "-") {
			version = atoiOr(a, 0)
		}
	}
	c := personaClient()
	if version == 0 {
		// No number means "back to what partyline ships", which also puts the mode back on the
		// release track. Worth saying out loud, because it is the difference between undoing one
		// edit and giving the persona back.
		if err := c.ResetPersona(key); err != nil {
			fatal(fmt.Errorf("ptln persona revert %s: %w", key, err))
		}
		fmt.Printf("  ✓ %s is back on the shipped default — it will track future releases again\n", key)
		return
	}
	if err := c.ActivatePersona(key, version); err != nil {
		fatal(fmt.Errorf("ptln persona revert %s %d: %w", key, version, err))
	}
	fmt.Printf("  ✓ %s is back on version %d\n", key, version)
}

// editInEditor opens text in $EDITOR and reads it back.
//
// Separate from cgEditorText (the compose field's version), which returns the text unchanged on
// failure because losing a half-written question would be unforgivable. Here a failure must be an
// ERROR: silently saving the text you started with, as though your edit had been applied, is worse
// than refusing.
func editInEditor(key, text string) (string, error) {
	ed := strings.TrimSpace(os.Getenv("EDITOR"))
	if ed == "" {
		return "", fmt.Errorf("$EDITOR is not set — `export EDITOR=nano` (or vim, or `code -w`) and try again")
	}
	dir, err := os.MkdirTemp("", "ptln-persona")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	// .md so an editor's soft-wrap and highlighting do something sensible with prose.
	path := filepath.Join(dir, key+".md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	// sh -c so $EDITOR may carry flags ("code -w", "emacsclient -nw"), as every other tool honours it.
	cmd := exec.Command("sh", "-c", ed+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("$EDITOR (%s) exited badly: %w — nothing was saved", ed, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func truncateOneLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
