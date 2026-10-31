package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"partyline.sh/partyline/internal/relayserver"
)

type relayHostConfig struct {
	Origin       string `json:"origin"`
	Listen       string `json:"listen"`
	Machine      string `json:"machine"`
	Hash         string `json:"hash"`
	Cert         string `json:"cert,omitempty"`
	Key          string `json:"key,omitempty"`
	ACME         bool   `json:"acme,omitempty"`
	TrustedProxy string `json:"trusted_proxy,omitempty"`
	Name         string `json:"name,omitempty"`
	Discover     bool   `json:"discover,omitempty"`
	RelayOnly    bool   `json:"relay_only,omitempty"`
}

func relayHostDir() string { return filepath.Join(relayDir(), "server") }
func loadRelayHost(dir string) (relayHostConfig, error) {
	var c relayHostConfig
	b, err := os.ReadFile(filepath.Join(dir, "server.json"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

func relayServe(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("relay serve", flag.ContinueOnError)
	data := f.String("data", relayHostDir(), "private relay data directory")
	origin := f.String("origin", "", "stable HTTPS origin used by computers and phones")
	listen := f.String("listen", "", "listen address; default 127.0.0.1:8787 behind your HTTPS proxy")
	init := f.Bool("init", false, "initialize this relay and register this computer (refuses an existing client configuration)")
	relayOnly := f.Bool("relay-only", false, "initialize a server-only gateway without changing this computer's relay connection")
	cert := f.String("tls-cert", "", "PEM certificate for HTTPS")
	key := f.String("tls-key", "", "PEM private key for HTTPS")
	acme := f.Bool("acme", false, "obtain HTTPS certificates using public ports 80 and 443; accepts Let's Encrypt terms")
	trustedProxy := f.String("trusted-proxy", "", "trusted proxy CIDRs; default loopback; none disables forwarded client IPs")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: ptln relay serve [--init --origin https://host] [--relay-only] [--data <dir>] [--listen <address>] [--tls-cert <file> --tls-key <file> | --acme]")
	}
	if *relayOnly && !*init {
		return errors.New("--relay-only applies only during --init; restart without it")
	}
	var c relayHostConfig
	if *init {
		var err error
		c, err = initializeRelayHost(*data, relayHostConfig{Origin: *origin, Listen: *listen, Cert: *cert, Key: *key, ACME: *acme, TrustedProxy: *trustedProxy, RelayOnly: *relayOnly})
		if err != nil {
			return err
		}
	} else {
		var err error
		c, err = loadRelayHost(*data)
		if err != nil {
			return fmt.Errorf("no relay host configured: use ptln relay serve --init --origin https://your-host: %w", err)
		}
		if *origin != "" && strings.TrimRight(*origin, "/") != c.Origin {
			return errors.New("changing the relay origin can invalidate pairing; configure a new host explicitly")
		}
		if *listen != "" {
			c.Listen = *listen
		}
		if *cert != "" {
			c.Cert = *cert
		}
		if *key != "" {
			c.Key = *key
		}
		if *acme {
			c.ACME = true
		}
		if *trustedProxy != "" {
			c.TrustedProxy = *trustedProxy
		}
	}
	if err := validateRelayHost(c); err != nil {
		return err
	}
	return runRelayHost(ctx, *data, c)
}

func validateRelayHost(c relayHostConfig) error {
	if err := relayserver.ValidateTrustedProxy(c.TrustedProxy); err != nil {
		return err
	}
	if _, err := newRelayConfig(c.Origin, c.Machine); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || port == "" {
		return errors.New("listen must be host:port")
	}
	if (c.Cert == "") != (c.Key == "") {
		return errors.New("HTTPS needs both --tls-cert and --tls-key")
	}
	if c.ACME && c.Cert != "" {
		return errors.New("choose ACME or certificate files, not both")
	}
	if c.ACME && port != "443" {
		return errors.New("ACME mode must listen on port 443; public port 80 must also reach this host")
	}
	ip := net.ParseIP(host)
	if c.Cert == "" && !c.ACME && (ip == nil || !ip.IsLoopback()) {
		return errors.New("plain HTTP may listen only on loopback behind a trusted HTTPS proxy; use TLS for LAN/public listeners")
	}
	return nil
}

func runRelayHost(parent context.Context, dir string, c relayHostConfig) error {
	lock, err := os.OpenFile(filepath.Join(dir, "serve.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("this private relay is already running")
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	relay, err := relayserver.Open(relayserver.Config{Database: filepath.Join(dir, "registry.sqlite"), Origin: c.Origin, InitialID: c.Machine, InitialHash: c.Hash, TrustedProxy: c.TrustedProxy})
	if err != nil {
		return err
	}
	defer relay.Close()
	srv := &http.Server{Addr: c.Listen, Handler: relay, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	var httpChallenge *http.Server
	if c.ACME {
		u, _ := relayURL(c.Origin)
		manager := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(u.Hostname()), Cache: autocert.DirCache(filepath.Join(dir, "certificates"))}
		if err = os.MkdirAll(filepath.Join(dir, "certificates"), 0700); err != nil {
			return err
		}
		srv.TLSConfig = manager.TLSConfig()
		httpChallenge = &http.Server{Addr: net.JoinHostPort("", "80"), Handler: manager.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second}
		listener, e := net.Listen("tcp", httpChallenge.Addr)
		if e != nil {
			return fmt.Errorf("ACME HTTP challenge port: %w", e)
		}
		go httpChallenge.Serve(listener)
		defer httpChallenge.Close()
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	if c.Discover {
		name := c.Name
		if name == "" {
			name, _ = os.Hostname()
		}
		advertisement, e := advertiseRelay(name, c.Origin)
		if e == nil {
			defer advertisement.Shutdown()
		} else {
			fmt.Fprintln(os.Stderr, "Local discovery unavailable:", e)
		}
	}
	fmt.Printf("Private relay listening on %s\nPublic address: %s\nData: %s\nKeep this host awake. Closing this foreground command stops the relay.\n", listener.Addr(), c.Origin, dir)
	failed := make(chan error, 1)
	go func() {
		if c.Cert != "" || c.ACME {
			failed <- srv.ServeTLS(listener, c.Cert, c.Key)
		} else {
			failed <- srv.Serve(listener)
		}
	}()
	select {
	case <-ctx.Done():
	case err = <-failed:
	}
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// initializeRelayHost only writes the reviewed setup. Starting the process or
// installing a login service is a separate explicit action.
func initializeRelayHost(dir string, c relayHostConfig) (relayHostConfig, error) {
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		return c, err
	}
	lock, err := os.OpenFile(filepath.Join(relayDir(), "host-setup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return c, err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return c, errors.New("another private gateway setup is running")
	}
	if _, err = os.Stat(filepath.Join(dir, "server.json")); !os.IsNotExist(err) {
		return c, errors.New("relay host already exists (or cannot be read); restart without --init")
	}
	if _, err = os.Stat(filepath.Join(relayDir(), "relay.json")); !os.IsNotExist(err) && !c.RelayOnly {
		return c, errors.New("this computer already has relay settings; initialization will not replace them")
	}
	id, err := relayRandom(16)
	if err != nil {
		return c, err
	}
	token, err := relayRandom(32)
	if err != nil {
		return c, err
	}
	client, err := newRelayConfig(strings.TrimSpace(c.Origin), id)
	if err != nil {
		return c, err
	}
	client.Hosting = "self-hosted"
	client.Assets = relayserver.AssetDigests()
	client.Sources = relaySourcesDigest()
	client.Version = version
	c.Origin = client.URL
	c.Machine = id
	c.Hash = relayserver.Digest(token)
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	if err = validateRelayHost(c); err != nil {
		return c, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return c, err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return c, err
	}
	if !st.IsDir() {
		return c, errors.New("private gateway data directory cannot be a symlink")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return c, err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err = relayPrivateFile(filepath.Join(dir, "owner-token"), []byte(token+"\n")); err != nil {
		return c, err
	}
	if err = relayPrivateFile(filepath.Join(dir, "server.json"), b); err != nil {
		return c, err
	}
	if !c.RelayOnly {
		if err = saveRelay(client, token); err != nil {
			_ = os.Remove(filepath.Join(dir, "server.json"))
			return c, err
		}
	}
	return c, nil
}
