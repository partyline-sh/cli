package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The update popup opens by itself, as the approval popup does: once per release, on the one
// attached client after it has been idle for updateIdleFor, when no other popup is open. esc (or a
// popup tmux closed) waits a day before asking again. With automatic updates on, the watcher
// installs the release instead and the popup opens only if that fails.

const (
	updateIdleFor = 30 * time.Second // longer than the approval popup's: nothing here is waiting on you
	updateLater   = 24 * time.Hour
)

type updatePrefs struct {
	Skip       string    `json:"skip,omitempty"` // a version not to ask about
	Auto       bool      `json:"auto,omitempty"`
	LaterUntil time.Time `json:"later_until,omitzero"`
	AutoTried  string    `json:"auto_tried,omitempty"` // the version automatic updating last tried
	AutoError  string    `json:"auto_error,omitempty"` // why that try failed
}

func updatePrefsPath() string    { return filepath.Join(stateDir(), "updates.json") }
func updatePopupPIDPath() string { return filepath.Join(stateDir(), "update-popup.pid") }
func updateLogPath() string      { return filepath.Join(stateDir(), "update.log") }

func loadUpdatePrefs() updatePrefs {
	var p updatePrefs
	if b, err := os.ReadFile(updatePrefsPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func saveUpdatePrefs(p updatePrefs) error {
	b, _ := json.MarshalIndent(p, "", "  ")
	tmp := updatePrefsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, updatePrefsPath())
}

func updatePopupOpen() bool {
	b, err := os.ReadFile(updatePopupPIDPath())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

// pendingUpdate is the newer release this machine should hear about, or "".
func pendingUpdate(c updateCache, p updatePrefs) string {
	if updateChecksDisabled() || c.Latest == "" || !versionLess(version, c.Latest) || p.Skip == c.Latest {
		return ""
	}
	return c.Latest
}

// updateOpener runs from the watcher's tick; its tmux calls and the update are variables in tests.
type updateOpener struct {
	clients func() ([]gateClient, error)
	open    func(c gateClient) error
	auto    func(latest string) error
}

func newUpdateOpener() *updateOpener {
	return &updateOpener{clients: tmuxClients, open: openUpdateOn, auto: autoUpdate}
}

func (o *updateOpener) tick(now time.Time) bool {
	p := loadUpdatePrefs()
	latest := pendingUpdate(readUpdateCache(), p)
	if latest == "" {
		return false
	}
	if p.Auto && p.AutoTried != latest {
		p.AutoTried, p.AutoError = latest, ""
		_ = saveUpdatePrefs(p)
		go func() {
			if err := o.auto(latest); err != nil {
				q := loadUpdatePrefs()
				q.AutoError = err.Error()
				_ = saveUpdatePrefs(q)
			}
		}()
		return false
	}
	if p.Auto && p.AutoError == "" {
		return false // installing, or installed and the watcher has not moved to it yet
	}
	if now.Before(p.LaterUntil) || updatePopupOpen() || gateOpen() || chatPopupOpen() {
		return false
	}
	cs, err := o.clients()
	if err != nil || len(cs) != 1 || now.Sub(cs[0].Activity) < updateIdleFor {
		return false
	}
	if o.open(cs[0]) != nil {
		return false
	}
	p.LaterUntil = now.Add(updateLater) // set on opening, so a popup tmux closed does not reopen at once
	_ = saveUpdatePrefs(p)
	return true
}

// openUpdateOn opens the popup centred on client without waiting for it to close.
func openUpdateOn(c gateClient) error {
	w, h := min(78, c.Width-2), min(26, c.Height-2)
	cmd := tmuxCmd("display-popup", "-c", c.Name, "-E", "-x", "C", "-y", "C", "-w", strconv.Itoa(max(w, 1)), "-h", strconv.Itoa(max(h, 1)),
		"-T", " update ", selfExe(), "_update")
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// autoUpdate installs latest with no one at the keyboard, logging to update.log, then lets the new
// binary refresh the tray. The watcher moves to the new binary by itself.
func autoUpdate(latest string) error {
	if why := autoUpdateBlocked(); why != "" {
		return fmt.Errorf("%s", why)
	}
	log, err := os.OpenFile(updateLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	fmt.Fprintf(log, "\n%s automatic update %s → %s\n", time.Now().Format(time.RFC3339), version, latest)
	cmd := upgradeCommand()
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(cmdEnv(cmd), "NONINTERACTIVE=1", "HOMEBREW_NO_AUTO_UPDATE=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the automatic update to %s failed (%v); %s has the output", latest, err, updateLogPath())
	}
	_ = exec.Command(selfExe(), "_tray-refresh").Run()
	return nil
}

func cmdEnv(cmd *exec.Cmd) []string {
	if cmd.Env != nil {
		return cmd.Env
	}
	return os.Environ()
}

// autoUpdateBlocked is why updating cannot run unattended here, or "": the install script needs
// to write this binary's directory without a password.
func autoUpdateBlocked() string {
	if installedViaBrew() {
		return ""
	}
	dir := installDir()
	if syscall.Access(dir, 2) != nil { // W_OK
		return "Automatic updates need to write " + dir + " without a password, and it needs sudo. Update now asks for the password here."
	}
	return ""
}

// installedVersion is what the binary on disk says it is ("ptln (partyline) 0.112.0"), without
// its network check.
func installedVersion() string {
	cmd := exec.Command(selfExe(), "version")
	cmd.Env = append(os.Environ(), "NO_UPDATE_NOTIFIER=1")
	out, _ := cmd.Output()
	f := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// updatePopupMain is `ptln _update`, the popup the watcher opens.
func updatePopupMain() {
	_ = os.WriteFile(updatePopupPIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer os.Remove(updatePopupPIDPath())
	c, p := readUpdateCache(), loadUpdatePrefs()
	latest := c.Latest
	if latest == "" || !versionLess(version, latest) {
		fmt.Println("partyline " + version + " is up to date.")
		return
	}
	how := "the install script"
	if installedViaBrew() {
		how = "Homebrew"
	}
	act := updateActions{
		update: func() tea.Cmd {
			return tea.ExecProcess(upgradeCommand(), func(err error) tea.Msg {
				if err != nil {
					return updateDone{err: err}
				}
				_ = exec.Command(selfExe(), "_tray-refresh").Run() // the new binary, now on disk
				return updateDone{installed: installedVersion()}
			})
		},
		skip: func(v string) error {
			q := loadUpdatePrefs()
			q.Skip = v
			return saveUpdatePrefs(q)
		},
		autoOn: func(on bool) error {
			q := loadUpdatePrefs()
			q.Auto, q.AutoTried, q.AutoError = on, "", ""
			return saveUpdatePrefs(q)
		},
		canAuto:  autoUpdateBlocked(),
		how:      how,
		autoNote: "On, the watcher installs each release by itself with " + how + ", " + latest + " included, and this popup stops appearing. It opens again only if an automatic update fails.",
	}
	m := newUpdatePrompt(latest, version, c.Changes, p.Auto, act)
	if p.AutoError != "" {
		m.err = p.AutoError
	}
	_, _ = tea.NewProgram(m).Run()
}
