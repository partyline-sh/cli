package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"partyline.sh/partyline/internal/api"
)

// `ptln review <work-item-id>` — mark up a work item's worked example, hand the marks to the model,
// and watch it rebuild the page without leaving the loop.
//
// WHY A LOCAL SERVER AND NOT THE WEB APP. The mockup is HTML an agent generated, which makes it
// untrusted: a prompt injection in the repo steers what the generator emits. Rendering it on
// partyline.sh would be stored XSS on every teammate's session. Here the blast radius is one local
// page, and the production origin never touches the bytes.
//
// Inference runs on the user's OWN engine, on this machine. partyline holds no API key — the same
// rule the describe flow follows, and the reason this is a command rather than a server job.

// The viewer is a small ES-module app, so the whole directory ships rather than named files.
// Modules resolve over HTTP against the local server, which is why they stay separate files instead
// of one unmaintainable blob.
//
//go:embed assets/review
var reviewAssets embed.FS

func reviewAsset(name string) []byte {
	b, err := reviewAssets.ReadFile("assets/review/" + name)
	if err != nil {
		// Embedded at build time — a miss is a build-integrity bug, not a runtime condition.
		panic("review asset missing: " + name)
	}
	return b
}

var (
	reviewViewerHTML = reviewAsset("viewer.html")
	reviewViewerJS   = reviewAsset("viewer.js")
	reviewSDKJS      = reviewAsset("sdk.js")
)

// The mockup document's own policy. 'unsafe-inline'/'unsafe-eval' are unavoidable — a mockup is
// inline styles and inline script by nature — but the two directives that matter are locked:
// connect-src 'none' means no fetch/XHR/WebSocket, and form-action 'none' means no POST out.
//
// img-src DOES allow https:, a deliberate and documented residual: mockups routinely pull
// placeholder images, and blocking them makes the tool useless for its main job. A mockup could
// therefore signal a few bits out through an image URL. That is an acceptable trade for a local
// review of your own agent's output; it would NOT be on a shared origin, which is the other reason
// this does not live in the web app.
const artifactCSP = "default-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
	"img-src 'self' data: blob: https:; font-src 'self' data: https:; " +
	"connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"

func reviewMain(args []string) {
	var itemID, localFile, model string
	var wantVersion, port int
	noOpen := false
	serve := false

	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--version" && i+1 < len(args):
			i++
			wantVersion, _ = strconv.Atoi(args[i])
		case a == "--port" && i+1 < len(args):
			i++
			port, _ = strconv.Atoi(args[i])
		case a == "--file" && i+1 < len(args):
			i++
			localFile = args[i]
		case a == "--model" && i+1 < len(args):
			i++
			model = args[i]
		case a == "--serve":
			serve = true
		case a == "--no-open":
			noOpen = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "ptln review: unknown flag %s\n", a)
			os.Exit(2)
		default:
			if itemID == "" {
				itemID = a
			}
		}
	}

	// The revision turn runs the user's own engine. Say so NOW rather than after they have spent ten
	// minutes marking a page up and pressed the button.
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "ptln review: `claude` is not on PATH — marking up still works, but the")
		fmt.Fprintln(os.Stderr, "  revision turn runs on your own engine and will fail until it is installed.")
	}

	if serve {
		c := api.New()
		if c.Token == "" {
			fmt.Fprintln(os.Stderr, "ptln review --serve: not signed in. Fix: ptln login")
			os.Exit(1)
		}
		cwd, _ := os.Getwd()
		h := newReviewHost(c, cwd, model)
		if port == 0 {
			port = reviewPort()
		}
		base, stop, err := h.start(port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ptln review --serve: %v\n", err)
			os.Exit(1)
		}
		defer stop()
		fmt.Printf("→ review host on %s\n", base)
		fmt.Printf("→ open a work item at %s/w/<work-item-id>\n", base)
		fmt.Println("→ Ctrl-C to stop")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println()
		return
	}

	if localFile != "" {
		reviewLocalFile(localFile, model, port, noOpen)
		return
	}
	if itemID == "" {
		fmt.Fprintln(os.Stderr, "ptln review: needs a work item id.")
		fmt.Fprintln(os.Stderr, "  ptln review <work-item-id>      open its latest worked example")
		fmt.Fprintln(os.Stderr, "  ptln review --file <page.html>  try the tools on a local file")
		fmt.Fprintln(os.Stderr, "  ptln review --serve             host every work item's example on one port")
		fmt.Fprintln(os.Stderr, "  Find ids with `ptln plan ls`, or on the board.")
		os.Exit(2)
	}

	c := api.New()
	if c.Token == "" {
		fmt.Fprintln(os.Stderr, "ptln review: not signed in. Fix: ptln login")
		os.Exit(1)
	}

	arts, err := c.ListArtifacts(itemID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln review: could not read this work item's examples: %v\n", err)
		fmt.Fprintln(os.Stderr, "  If that says not found, check the id — and run `ptln doctor` if the rest of partyline is misbehaving.")
		os.Exit(1)
	}
	if len(arts) == 0 {
		fmt.Fprintln(os.Stderr, "ptln review: this work item has no worked example yet.")
		fmt.Fprintln(os.Stderr, "  An agent publishes one during planning; ask it to draft the mockup first.")
		os.Exit(1)
	}

	art := arts[0] // newest first
	if wantVersion > 0 {
		found := false
		for _, a := range arts {
			if a.Version == wantVersion {
				art, found = a, true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "ptln review: no version %d (latest is v%d).\n", wantVersion, arts[0].Version)
			os.Exit(1)
		}
	}

	html, err := c.FetchArtifact(itemID, art.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln review: could not fetch the example's html: %v\n", err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	s := newReviewServer(html, art.Version)
	s.client, s.itemID, s.artID, s.model, s.dir = c, itemID, art.ID, model, cwd
	runReviewLoop(s, art.Title, port, noOpen)
}

// reviewLocalFile marks up an HTML file on disk. Nothing is fetched from the control plane and
// nothing is saved — but the revision loop still runs, so a mockup can be iterated on before it has
// a work item at all.
func reviewLocalFile(path, model string, port int, noOpen bool) {
	html, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln review: cannot read %s: %v\n", path, err)
		os.Exit(1)
	}
	cwd, _ := os.Getwd()
	s := newReviewServer(html, 0)
	s.model, s.dir = model, cwd
	runReviewLoop(s, path, port, noOpen)
	fmt.Fprintln(os.Stderr, "(local preview — nothing was saved to a work item)")
}

func runReviewLoop(s *reviewServer, title string, port int, noOpen bool) {
	url, stop, err := s.listenAndServe(port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln review: %v\n", err)
		os.Exit(1)
	}
	defer stop()

	if title == "" {
		title = "worked example"
	}
	fmt.Printf("→ %s (v%d) at %s\n", title, s.version, url)
	fmt.Println("→ mark it up — Send hands your marks to the model and rebuilds the page here")
	fmt.Println("→ Finish in the page, or Ctrl-C, when you're done")
	if !noOpen {
		openBrowser(url)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	select {
	case <-s.done:
	case <-sig:
		fmt.Println()
	}

	marks, rounds := s.tally()
	if marks == 0 {
		fmt.Println("No marks recorded — nothing was changed.")
		return
	}
	fmt.Printf("✓ %d mark(s) over %d revision(s); now at v%d\n", marks, rounds, s.version)
}
