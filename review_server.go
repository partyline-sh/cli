package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The local review server. It holds the CURRENT mockup, streams the model's progress to the page
// while it revises, and swaps in the new version when the turn lands — so the human never leaves the
// loop to see what their marks did.
//
// The artifact is agent-generated HTML and therefore untrusted. It is framed with
// sandbox="allow-scripts" and NO allow-same-origin (opaque origin — it cannot read the page around
// it) under a CSP that forbids connect-src outright. That holds for every regenerated version too:
// the bytes change, the containment does not.
type reviewServer struct {
	mu      sync.Mutex
	html    []byte
	version int
	busy    bool
	subs    map[chan []byte]struct{}

	client *api.Client // nil in --file mode: nothing is saved and nothing is published
	itemID string
	artID  string
	model  string
	dir    string

	doneOnce sync.Once
	done     chan struct{}

	marks  int // total recorded across the session, for the closing line
	rounds int
}

// tally reports what the session actually achieved. Read under the lock because a revision may still
// be finishing when Ctrl-C lands.
func (s *reviewServer) tally() (marks, rounds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marks, s.rounds
}

func newReviewServer(html []byte, version int) *reviewServer {
	return &reviewServer{html: html, version: version, subs: map[chan []byte]struct{}{}, done: make(chan struct{})}
}

func (s *reviewServer) finish() { s.doneOnce.Do(func() { close(s.done) }) }

// ---- event fan-out ---------------------------------------------------------

type reviewEvent struct {
	Type    string `json:"type"` // activity | status | reload | error | done
	Text    string `json:"text,omitempty"`
	Version int    `json:"version,omitempty"`
}

func (s *reviewServer) emit(ev reviewEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		// Never block on a browser that stopped reading — a wedged tab must not stall the model's
		// output or the CLI behind it. A dropped activity line is cosmetic; a deadlock is not.
		select {
		case ch <- b:
		default:
		}
	}
}

func (s *reviewServer) subscribe() chan []byte {
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *reviewServer) unsubscribe(ch chan []byte) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
	close(ch)
}

// ---- handlers --------------------------------------------------------------

func (s *reviewServer) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(reviewViewerHTML)
	})

	// Any embedded module. The name is checked against the embed FS, which holds exactly the files
	// shipped in the binary — there is no directory to traverse out of.
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

	// The current mockup, with the courier prepended. Served as its own document rather than inlined
	// into a srcdoc: no escaping to get wrong, and the iframe's sandbox attribute gives it an opaque
	// origin regardless of it coming from the same port.
	mux.HandleFunc("/artifact", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		html := s.html
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", artifactCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The frame is reloaded in place after every revision, so a cached copy would show the human
		// the version they just changed and let them mark it up again.
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte("<script>"))
		w.Write(reviewSDKJS)
		w.Write([]byte("</script>\n"))
		w.Write(html)
	})

	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/marks", s.handleMarks)
	mux.HandleFunc("/finish", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
		s.finish()
	})

	return mux
}

func (s *reviewServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	// A heartbeat keeps the connection from being reaped by an idle proxy or a sleeping tab, and it
	// is how this loop notices the browser went away.
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (s *reviewServer) handleMarks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	var in struct {
		Marks []api.Annotation `json:"marks"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if len(in.Marks) == 0 {
		http.Error(w, "no marks", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		// One turn at a time. Two concurrent revisions of the same page would race to replace it and
		// the loser's work would vanish with no trace, which reads as "my marks did nothing".
		http.Error(w, "a revision is already running", http.StatusConflict)
		return
	}
	s.busy = true
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"regenerating":true}`))

	go s.revise(in.Marks)
}

// ---- the loop --------------------------------------------------------------

// revise saves the marks, asks the user's own engine for a new version, and swaps it in.
//
// SAVING COMES FIRST and its failure is fatal to the turn. The marks are the human's work; the
// regenerated page is derivable from them. Regenerating against marks we failed to record would
// produce a mockup nobody can trace back to a requirement.
func (s *reviewServer) revise(marks []api.Annotation) {
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	if s.client != nil {
		s.emit(reviewEvent{Type: "status", Text: "saving marks…"})
		if _, err := s.client.PostMarks(s.itemID, s.artID, marks); err != nil {
			s.emit(reviewEvent{Type: "error", Text: "could not save the marks: " + err.Error() + " — nothing was changed"})
			return
		}
	}

	s.mu.Lock()
	current := string(s.html)
	s.mu.Unlock()

	s.emit(reviewEvent{Type: "status", Text: fmt.Sprintf("revising with %d mark(s)…", len(marks))})

	sink := reviewSink(func(line string) {
		s.emit(reviewEvent{Type: "activity", Text: line})
		fmt.Printf("   %s\n", line) // the CLI shows the same stream, for anyone watching the terminal
	})

	next, err := regenerate(context.Background(), s.dir, s.model, current, marks, sink)
	if err != nil {
		// The previous version stays mounted on purpose — a failed turn must not leave the human
		// staring at a blank or half-built page wondering which one they are looking at.
		s.emit(reviewEvent{Type: "error", Text: err.Error() + " — the previous version is still shown"})
		fmt.Printf("   revision failed: %v\n", err)
		return
	}

	version := s.version + 1
	if s.client != nil {
		s.emit(reviewEvent{Type: "status", Text: "publishing the new version…"})
		art, err := s.client.PublishArtifact(s.itemID, next, "", fmt.Sprintf("revised from %d mark(s)", len(marks)))
		if err != nil {
			s.emit(reviewEvent{Type: "error", Text: "revised, but could not publish: " + err.Error()})
			return
		}
		// Point subsequent marks at the version they were actually made on.
		s.artID, version = art.ID, art.Version
	}

	s.mu.Lock()
	s.html = []byte(next)
	s.version = version
	s.marks += len(marks)
	s.rounds++
	s.mu.Unlock()

	s.emit(reviewEvent{Type: "reload", Version: version})
	fmt.Printf("✓ v%d ready — reloaded in the browser\n", version)
}

// reviewSink filters the engine's stream down to progress a human wants to watch.
//
// The model's answer IS an entire HTML document, so an unfiltered stream pours the page source into
// the activity rail — hundreds of lines of markup and CSS that bury the two or three lines saying
// what is actually happening, and scroll them away at the exact moment the human is waiting to see
// whether their mark was understood.
//
// DOCUMENT MODE IS STICKY, and that is the lesson from watching a real turn: the opening ``` fence
// does not reliably arrive as its own line, so a filter that depends on seeing one leaks everything
// after it. The document is the last thing the model produces, so once any line looks like the
// document has begun, everything after it is suppressed. Prose after the document would be lost —
// there is none worth keeping, because the document IS the answer.
func reviewSink(out func(string)) func(string) {
	inDoc := false
	last := ""
	return func(line string) {
		line = strings.TrimSpace(line)
		if line == "" || inDoc {
			return
		}
		if strings.HasPrefix(line, "```") || looksLikeMarkup(line) {
			inDoc = true
			out("writing the revised document…")
			return
		}
		// Engines repeat step markers (a dozen identical "thinking" lines is normal). Repetition
		// carries no information and pushes the informative lines off screen.
		if line == last {
			return
		}
		last = line
		out(line)
	}
}

// looksLikeMarkup is deliberately blunt: it decides what to HIDE from a progress log, so a false
// positive costs one unseen status line while a false negative costs a screenful of page source.
func looksLikeMarkup(line string) bool {
	switch {
	case strings.HasPrefix(line, "<"), strings.HasPrefix(line, "/*"), strings.HasPrefix(line, "}"):
		return true
	// A CSS rule ends in "}" and a selector opens with "{" — both were leaking before, because the
	// only cases covered were the ones a fenced block would already have caught.
	case strings.HasSuffix(line, "{"), strings.HasSuffix(line, "}"), strings.HasSuffix(line, ";"), strings.HasSuffix(line, ">"):
		return true
	}
	return false
}

// ---- lifecycle -------------------------------------------------------------

func (s *reviewServer) listenAndServe(port int) (string, func(), error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", nil, fmt.Errorf("could not open a local port: %w", err)
	}
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	return url, func() { ln.Close() }, nil
}
