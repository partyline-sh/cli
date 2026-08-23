package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The review HOST: one long-lived local server that can show any work item's worked example.
//
// WHY A HOST AND NOT A SERVER PER REVIEW. Nobody types `ptln review <id>`. The planning agent hands
// you a link mid-conversation, and a link only works if something is already listening — a design
// where the web must first ask a daemon to start a process, wait for it to report a port, and only
// then produce a URL is three round trips before the human can look at anything, and every one of
// them can fail while they are waiting.
//
// So the daemon holds ONE port and serves /w/<work-item-id>. The agent can print the URL the moment
// it publishes an artifact, with nothing to coordinate: the link is a pure function of the work item
// id and the port. Nothing is started on demand and nothing has to be torn down.
//
// LOOPBACK ONLY, and that is the security boundary. It binds 127.0.0.1, so the only people who can
// reach it are people already on the machine. It uses the machine owner's OWN token to fetch
// artifacts, so it can never show anything they could not already see. Nothing from the control
// plane becomes a path, a port or an argument.
const defaultReviewPort = 7391

// reviewHost is the shared listener. Per-item state is created lazily on first view and kept, so a
// reload does not lose marks the human has not sent yet.
type reviewHost struct {
	mu    sync.Mutex
	items map[string]*reviewServer // work item id → its review session
	c     *api.Client
	dir   string // where the engine runs for a revision turn
	model string
}

func newReviewHost(c *api.Client, dir, model string) *reviewHost {
	return &reviewHost{items: map[string]*reviewServer{}, c: c, dir: dir, model: model}
}

// reviewPort is the port the host listens on. Fixed rather than ephemeral BECAUSE the URL has to be
// predictable: an agent composes it from the work item id alone, without asking anything.
func reviewPort() int {
	if v := strings.TrimSpace(os.Getenv("PARTYLINE_REVIEW_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return defaultReviewPort
}

// ReviewURL is the address for a work item's review, composed the same way everywhere — the CLI
// prints it, the agent hands it over, the web links to it. One function so the three can never
// disagree about the shape.
func ReviewURL(workItemID string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/w/%s", reviewPort(), workItemID)
}

// session returns the review for a work item, creating it on first view.
//
// Fetching the artifact LAZILY is what makes the link work with no coordination: the agent can
// publish a version and hand over the URL in the same breath, and the first person to open it gets
// whatever the newest version is at that moment.
func (h *reviewHost) session(workItemID string) (*reviewServer, error) {
	h.mu.Lock()
	if s, ok := h.items[workItemID]; ok {
		h.mu.Unlock()
		return s, nil
	}
	h.mu.Unlock()

	arts, err := h.c.ListArtifacts(workItemID)
	if err != nil {
		return nil, fmt.Errorf("could not read this work item's examples: %w", err)
	}
	if len(arts) == 0 {
		return nil, fmt.Errorf("this work item has no worked example yet")
	}
	art := arts[0] // newest first
	html, err := h.c.FetchArtifact(workItemID, art.ID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the example: %w", err)
	}

	s := newReviewServer(html, art.Version)
	s.client, s.itemID, s.artID, s.model, s.dir = h.c, workItemID, art.ID, h.model, h.dir

	h.mu.Lock()
	defer h.mu.Unlock()
	// Another request may have built one while we were fetching; keep the first so two tabs share
	// a session rather than silently holding separate copies of the same marks.
	if existing, ok := h.items[workItemID]; ok {
		return existing, nil
	}
	h.items[workItemID] = s
	return s, nil
}

func (h *reviewHost) handler() http.Handler {
	mux := http.NewServeMux()

	// Shared assets, mounted once at the top level so every review references the same URL.
	mux.HandleFunc("/js/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/js/")
		if name == "" || strings.Contains(name, "/") || !strings.HasSuffix(name, ".js") {
			http.NotFound(w, r)
			return
		}
		b, err := reviewAssets.ReadFile("assets/review/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(b)
	})

	mux.HandleFunc("/w/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/w/")
		id, sub, _ := strings.Cut(rest, "/")
		if !looksLikeWorkItemID(id) {
			http.Error(w, "not a work item id", http.StatusBadRequest)
			return
		}
		s, err := h.session(id)
		if err != nil {
			// Plain text, not a redirect: whoever opened this link is a human reading a page, and
			// the useful thing is the reason, not a bounce to somewhere with less information.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "Cannot show a review for %s.\n\n%v\n\nAn agent publishes a worked example during planning; ask it to draft the mockup first.\n", id, err)
			return
		}
		// The per-item routes are written as if they were at the root, so the same mux serves a
		// standalone `ptln review` unchanged. The viewer derives its base from the URL to match.
		http.StripPrefix("/w/"+id, s.routes()).ServeHTTP(w, r)
		_ = sub
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "partyline review host.\n\nOpen a work item's worked example at:\n  %s\n", ReviewURL("<work-item-id>"))
	})

	return mux
}

// looksLikeWorkItemID keeps anything that is not an id out of the path before it is used to build a
// request. Deliberately strict: ids are uuids, and the only reason to send anything else here is to
// find out what happens.
func looksLikeWorkItemID(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// serveReviewHost runs the host until ctx-less shutdown. Returns the listener so a caller can close
// it; blocks nothing.
func (h *reviewHost) start(port int) (string, func(), error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", nil, fmt.Errorf("could not open the review port %d: %w", port, err)
	}
	srv := &http.Server{Handler: h.handler(), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	return fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port), func() { ln.Close() }, nil
}
