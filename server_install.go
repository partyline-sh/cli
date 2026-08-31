package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"strconv"
	"strings"
	"time"
)

// server_install.go — `ptln server install`: bring a self-hosted partyline up on THIS machine.
//
// THIS REVERSES AN EARLIER DECISION, DELIBERATELY. server_bootstrap.go states that bootstrap is a
// REFERENCE and never acts, because "a tool that half-installs a box leaves an operator debugging
// OUR partial state instead of their own machine". That reasoning was right about the failure mode
// and wrong about the conclusion: the fix for a half-install is not to refuse to install, it is to
// make every step assert its own effect and to make a re-run reconcile. `ptln server bootstrap` is
// unchanged and still prints a plan for operators who want to run it themselves; install is the
// door for everyone else.
//
// FIVE PHASES, IN ORDER. Each one is allowed to do strictly less than the next:
//
//	preflight  reads the machine. Writes NOTHING, ever, including on the happy path.
//	plan       prints what will happen, in the order it will happen. --dry-run stops here.
//	apply      does it, asserting after each step that the step actually took effect.
//	verify     checks the RESULT end-to-end, not the steps — a box can pass every step and
//	           still not serve a page.
//	report     says what was written and what to do next.
//
// IDEMPOTENT. Re-running reconciles: stack files are rewritten only when their content differs,
// .env is only ever ADDED to (env-bootstrap.sh's own guarantee), and compose converges. Re-running
// on a healthy box is a no-op that still verifies.
//
// NOT WEB-TRIGGERABLE. There is no daemon path to this command and there must never be one — it
// writes to a system directory and starts containers, which is exactly the authority the
// reference-not-command invariant keeps out of the control plane's hands.

// installConfig is every choice the installer makes. Flags and prompts both land here, so the plan
// can be printed from one struct and the apply phase has no other source of truth.
type installConfig struct {
	dir       string // where the stack lives
	site      string // public URL, e.g. https://partyline.example.com
	bind      string // host interface to publish on
	httpPort  int
	httpsPort int
	relayPort int
	noCaddy   bool // don't run the edge; something else terminates TLS
	minio     bool // run the bundled MinIO; default ON — --no-minio or the storage row turns it off
	tls       tlsMode

	// dns is an OPTIONAL resolver to use instead of the system one, for a name that only exists
	// in an internal zone. It is used twice: to check the site resolves during setup, and by the
	// containers at runtime — the web container resolves SITE_URL itself to fetch OIDC
	// discovery, so a split-horizon name the host can see and the container cannot is an
	// install that passes every step and cannot sign anybody in.
	dns       string
	dryRun    bool
	assumeYes bool

	// explicit records which .env names the operator set ON THIS RUN, so that a flag can
	// override a value already in .env while a mere default never does.
	explicit map[string]bool
}

// installOps is every side effect the installer can have, injected so that a test can assert the
// preflight and plan phases write nothing at all — a claim that is worthless if it depends on what
// the machine running the test happens to have installed.
type installOps struct {
	mkdirAll  func(string, os.FileMode) error
	writeFile func(string, []byte, os.FileMode) error
	stat      func(string) (os.FileInfo, error)
	run       func(dir string, name string, args ...string) (string, error)
	lookPath  func(string) (string, error)
	portBusy  func(bind string, port int) bool
	lookup    func(host string) ([]string, error)                // DNS resolution for the site name
	localIPs  func() []string                                    // this machine's own addresses
	sleep     func(time.Duration)                                // injected so the DNS watch is testable
	runToFile func(dir, path, name string, args ...string) error // stdout streamed to a file (the pg_dump)
	httpOK    func(url string) (int, error)
	out       io.Writer
	in        *bufio.Reader
}

func liveInstallOps() installOps {
	return installOps{
		mkdirAll:  os.MkdirAll,
		writeFile: os.WriteFile,
		stat:      os.Stat,
		run: func(dir, name string, args ...string) (string, error) {
			cmd := exec.Command(name, args...)
			cmd.Dir = dir
			b, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(b)), err
		},
		lookPath:  exec.LookPath,
		portBusy:  livePortBusy,
		lookup:    net.LookupHost,
		localIPs:  localIPs,
		sleep:     time.Sleep,
		runToFile: runToFileLive,
		httpOK:    liveHTTPStatus,
		out:       os.Stdout,
		in:        bufio.NewReader(os.Stdin),
	}
}

func livePortBusy(bind string, port int) bool {
	host := bind
	if host == "" || host == "0.0.0.0" {
		host = ""
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func defaultInstallConfig() installConfig {
	return installConfig{
		explicit:  map[string]bool{},
		minio:     true,
		dir:       liveDefaultInstallDir(),
		bind:      "0.0.0.0",
		httpPort:  80,
		httpsPort: 443,
		relayPort: 2222,
	}
}

func serverInstallMain(args []string) {
	cfg := defaultInstallConfig()
	var err error
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ptln server install: %s needs a value\n", a)
				os.Exit(2)
			}
			i++
			return args[i]
		}
		switch {
		case a == "--help", a == "-h", a == "help":
			serverInstallHelp(os.Stdout)
			return
		case a == "--dry-run":
			cfg.dryRun = true
		case a == "--yes", a == "-y":
			cfg.assumeYes = true
		case a == "--tls":
			cfg.tls = tlsMode(next())
		case strings.HasPrefix(a, "--tls="):
			cfg.tls = tlsMode(strings.TrimPrefix(a, "--tls="))
		case a == "--dns":
			cfg.dns = next()
		case strings.HasPrefix(a, "--dns="):
			cfg.dns = strings.TrimPrefix(a, "--dns=")
		case a == "--with-minio": // kept: it shipped in v0.90.0 while off was the default
			cfg.minio = true
			cfg.explicit["MINIO_REPLICAS"] = true
		case a == "--no-minio":
			cfg.minio = false
			cfg.explicit["MINIO_REPLICAS"] = true
		case a == "--no-caddy":
			cfg.noCaddy = true
			cfg.explicit["CADDY_REPLICAS"] = true
		case a == "--dir":
			cfg.dir = next()
		case strings.HasPrefix(a, "--dir="):
			cfg.dir = strings.TrimPrefix(a, "--dir=")
		case a == "--site":
			cfg.site = next()
		case strings.HasPrefix(a, "--site="):
			cfg.site = strings.TrimPrefix(a, "--site=")
		case a == "--bind":
			cfg.explicit["BIND_ADDR"] = true
			cfg.bind = next()
		case strings.HasPrefix(a, "--bind="):
			cfg.explicit["BIND_ADDR"] = true
			cfg.bind = strings.TrimPrefix(a, "--bind=")
		case a == "--http-port":
			cfg.explicit["HTTP_PORT"] = true
			cfg.httpPort, err = strconv.Atoi(next())
		case strings.HasPrefix(a, "--http-port="):
			cfg.explicit["HTTP_PORT"] = true
			cfg.httpPort, err = strconv.Atoi(strings.TrimPrefix(a, "--http-port="))
		case a == "--https-port":
			cfg.explicit["HTTPS_PORT"] = true
			cfg.httpsPort, err = strconv.Atoi(next())
		case strings.HasPrefix(a, "--https-port="):
			cfg.explicit["HTTPS_PORT"] = true
			cfg.httpsPort, err = strconv.Atoi(strings.TrimPrefix(a, "--https-port="))
		case a == "--relay-port":
			cfg.explicit["RELAY_PORT"] = true
			cfg.relayPort, err = strconv.Atoi(next())
		case strings.HasPrefix(a, "--relay-port="):
			cfg.explicit["RELAY_PORT"] = true
			cfg.relayPort, err = strconv.Atoi(strings.TrimPrefix(a, "--relay-port="))
		default:
			fmt.Fprintf(os.Stderr, "ptln server install: unknown flag %q\n\n", a)
			serverInstallHelp(os.Stderr)
			os.Exit(2)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ptln server install: %s takes a port number\n", a)
			os.Exit(2)
		}
	}

	switch cfg.tls {
	case "", tlsAuto, tlsACME, tlsInternal, tlsOff:
	default:
		fmt.Fprintf(os.Stderr, "ptln server install: --tls takes auto, acme, internal or off (got %q)\n", cfg.tls)
		os.Exit(2)
	}

	if !runInstall(cfg, liveInstallOps()) {
		os.Exit(1)
	}
}

func serverInstallHelp(w io.Writer) {
	fmt.Fprint(w, `ptln server install — bring a self-hosted partyline up on this machine.

  ptln server install --site https://partyline.example.com

Five phases: preflight (reads only) → plan → apply → verify → report.
Safe to re-run: it reconciles, and never rewrites the .env holding your secrets.

  --site URL         public URL this box will serve (prompted if omitted)
  --dir PATH         where the stack lives (default: /opt/partyline if you can write
                     it, else ~/partyline)
  --bind ADDR        host interface to publish on (default 0.0.0.0)
  --http-port N      host port for HTTP  (default 80)
  --https-port N     host port for HTTPS (default 443)
  --relay-port N     host port for the relay (default 2222)
  --dns ADDR         resolver for a name that only exists in an internal zone; the
                     containers use it too (optional — system resolver otherwise)
  --tls MODE         auto (default), acme, internal, or off
  --no-minio         skip the bundled MinIO — attachments stay dark and two
                     fewer containers run (storage is ON by default)
  --no-caddy         don't run the edge — something else terminates TLS
  --dry-run          print the plan and stop, writing nothing
  --yes, -y          don't prompt; take the flags and defaults as given

No public domain? That is the normal case, and it works: with --tls auto a name
that no public CA can issue for (an IP, a .local, a Tailscale name, a bare
hostname) gets a certificate from Caddy's own CA instead. HTTPS works offline;
browsers warn until you trust that CA. --tls off serves plain HTTP.

Moving HTTP/HTTPS off 80/443 costs you a Let's Encrypt certificate, which is
validated on the public 80 or 443 — but not --tls internal, which needs
nothing from the internet.
`)
}
