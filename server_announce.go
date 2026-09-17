package main

// `ptln server announce` — the host half of LAN discovery (epic PA · P2).
//
// Containers on a bridge network cannot multicast to the LAN, so the advertisement runs on
// the HOST: this command reads the install's own records (site URL, directory) and announces
// _partyline._tcp until stopped. `ptln server install`/`update` install it as a small systemd
// unit on Linux; anywhere else it can simply be run in a shell.
//
// Announcing is not admitting: discovery names the instance to CLIs on the network, and the
// device flow still decides who gets in.

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"partyline.sh/partyline/internal/discover"
)

func serverAnnounceMain(args []string) {
	oneshot := len(args) > 0 && args[0] == "--print-unit"
	site := localInstanceSite()
	if site == "" {
		fatal(fmt.Errorf("ptln server announce: no partyline install on this box (PARTYLINE_DIR points at one elsewhere)"))
	}
	if oneshot {
		fmt.Print(announceUnit())
		return
	}
	port := 443
	if u, err := url.Parse(site); err == nil && u.Port() != "" {
		if n, err := strconv.Atoi(u.Port()); err == nil {
			port = n
		}
	}
	host, _ := os.Hostname()
	stop, err := discover.Announce(site, host, port)
	if err != nil {
		fatal(fmt.Errorf("ptln server announce: %w", err))
	}
	defer stop()
	fmt.Printf("☎ announcing %s as %q on the local network (_partyline._tcp) — ctrl-c stops\n", site, host)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func announceUnit() string {
	exe := selfExe()
	return `[Unit]
Description=partyline LAN announce (_partyline._tcp)
After=network-online.target

[Service]
ExecStart=` + exe + ` server announce
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`
}

// installAnnounceUnit wires the announcement into systemd, best-effort: a box without
// systemd (or a refused write) just skips it, and `ptln server announce` still works by hand.
func installAnnounceUnit(out *os.File) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return
	}
	const unitPath = "/etc/systemd/system/partyline-announce.service"
	if err := os.WriteFile(unitPath, []byte(announceUnit()), 0o644); err != nil {
		return
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	if err := exec.Command("systemctl", "enable", "--now", "partyline-announce.service").Run(); err == nil {
		fmt.Fprintf(out, "(LAN discovery on: this box announces itself — bare `ptln login` on this network finds it)\n")
	}
}
