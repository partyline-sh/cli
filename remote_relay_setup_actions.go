package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func tailscaleRelayOrigin() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", errors.New("Tailscale is not connected. Install/sign in to Tailscale on this gateway first, or choose your existing HTTPS proxy")
	}
	var status struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if json.Unmarshal(out, &status) != nil || status.BackendState != "Running" || status.Self.DNSName == "" {
		return "", errors.New("Tailscale is not ready; connect it before continuing")
	}
	origin := "https://" + strings.TrimSuffix(status.Self.DNSName, ".")
	if _, err := relayURL(origin); err != nil {
		return "", err
	}
	return origin, nil
}
func relaySetup() error {
	model := newRelaySetupModel()
	if requireUnconfiguredRelay() != nil {
		model.existing = true
		model.selection = 1
		model.note = "Your existing relay connection will be kept. A private gateway can run independently on this computer."
	}
	final, err := tea.NewProgram(model).Run()
	if err != nil {
		return err
	}
	action := final.(relaySetupModel).action
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch action.Kind {
	case "cloudflare":
		return relayDeploy(ctx)
	case "invite":
		if err = relayJoin(ctx, action.Invite); err == nil {
			ensureWatcher()
		}
		return err
	case "join":
		decoded, decodeErr := hex.DecodeString(action.Token)
		if decodeErr != nil || len(decoded) != 32 {
			return errors.New("join was not verified; no registration was saved")
		}
		c, e := newRelayConfig(action.Origin, action.Machine)
		if e != nil {
			return e
		}
		c.Hosting = "self-hosted"
		if err = saveNewRelay(c, action.Token); err == nil {
			ensureWatcher()
		}
		return err
	case "host":
		relayOnly := requireUnconfiguredRelay() != nil
		_, err = initializeRelayHost(relayHostDir(), relayHostConfig{Origin: action.Origin, Name: action.Name, Discover: true, RelayOnly: relayOnly})
		if err != nil {
			return err
		}
		if err = relayServiceStart(ctx); err != nil {
			return fmt.Errorf("gateway settings saved, but its background service did not start: %w", err)
		}
		if action.Route == "tailscale" {
			out, e := exec.CommandContext(ctx, "tailscale", "serve", "--bg", "http://127.0.0.1:8787").CombinedOutput()
			if e != nil {
				return fmt.Errorf("gateway service started, but Tailscale HTTPS is not configured: %s", strings.TrimSpace(string(out)))
			}
		}
		c, token, e := loadRelayOwner()
		if e != nil {
			return e
		}
		proxy, e := awaitRelayGateway(ctx, c, token, 10*time.Second)
		if e != nil {
			return fmt.Errorf("gateway settings saved, but authenticated HTTPS is not reachable yet; open ptln relay to inspect the private gateway: %w", e)
		}
		if proxy == "shared" {
			return errors.New("gateway is running, but its HTTPS proxy does not send client IPs. Configure it to append or overwrite X-Forwarded-For before sharing the relay")
		}
		ensureWatcher()
		fmt.Println("Private gateway is reachable. Other computers can try local discovery or enter its HTTPS address; approve them with ptln relay approve, then pair the phone on each target computer. Existing client connections were kept.")
	}
	return nil
}

func requireUnconfiguredRelay() error {
	if _, err := os.Lstat(filepath.Join(relayDir(), "relay.json")); !os.IsNotExist(err) {
		return errors.New("this computer already has relay settings (or they cannot be read); they will not be replaced. Open ptln relay to inspect them")
	}
	return nil
}
func saveNewRelay(c relayConfig, token string) error {
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(relayDir(), "host-setup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another relay setup is running")
	}
	if err = requireUnconfiguredRelay(); err != nil {
		return err
	}
	return saveRelay(c, token)
}
