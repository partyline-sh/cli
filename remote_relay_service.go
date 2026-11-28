package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const relayServiceLabel = "sh.partyline.relay"

type relayServiceSpec struct {
	Path, Contents, Why string
	Start, Stop, Status [][]string
}

func systemdArg(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`, "\n", `\n`, "\r", `\r`).Replace(value) + `"`
}
func relayServiceSpecFor(platform, home, exe, data string, uid int) (relayServiceSpec, error) {
	var s relayServiceSpec
	switch platform {
	case "darwin":
		s.Path = filepath.Join(home, "Library", "LaunchAgents", relayServiceLabel+".plist")
		s.Contents = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>relay</string><string>serve</string><string>--data</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
<key>Umask</key><integer>63</integer></dict></plist>
`, relayServiceLabel, html.EscapeString(exe), html.EscapeString(data), html.EscapeString(filepath.Join(data, "service.log")), html.EscapeString(filepath.Join(data, "service.log")))
		target := "gui/" + strconv.Itoa(uid)
		s.Start = [][]string{{"launchctl", "bootstrap", target, s.Path}}
		s.Stop = [][]string{{"launchctl", "bootout", target + "/" + relayServiceLabel}}
		s.Status = [][]string{{"launchctl", "print", target + "/" + relayServiceLabel}}
		s.Why = "Runs at login and stays running when you close the terminal. Sleeping this Mac still interrupts access."
	case "linux":
		s.Path = filepath.Join(home, ".config", "systemd", "user", "partyline-relay.service")
		s.Contents = fmt.Sprintf("[Unit]\nDescription=Partyline private gateway\n\n[Service]\nType=exec\nExecStart=%s relay serve --data %s\nRestart=on-failure\nRestartSec=5\nUMask=0077\nNoNewPrivileges=true\n\n[Install]\nWantedBy=default.target\n", systemdArg(exe), systemdArg(data))
		s.Start = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "partyline-relay.service"}}
		s.Stop = [][]string{{"systemctl", "--user", "disable", "--now", "partyline-relay.service"}}
		s.Status = [][]string{{"systemctl", "--user", "status", "--no-pager", "partyline-relay.service"}}
		s.Why = "Runs as your user. To survive logout/boot on a server, an administrator must enable user lingering; this setup does not change that permission."
	default:
		return s, errors.New("background relay services are supported on macOS and Linux; use ptln relay serve in the foreground here")
	}
	return s, nil
}
func relayServiceSpecCurrent() (relayServiceSpec, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return relayServiceSpec{}, err
	}
	return relayServiceSpecFor(runtime.GOOS, home, relayServiceExecutable(selfExe()), relayHostDir(), os.Getuid())
}
func runRelayServiceCommands(ctx context.Context, commands [][]string) error {
	for _, args := range commands {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return &relayServiceCommandError{Command: args[0], Err: err, Output: strings.TrimSpace(string(output))}
		}
	}
	return nil
}
func relayServiceStart(ctx context.Context) (err error) {
	if _, err := loadRelayHost(relayHostDir()); err != nil {
		return errors.New("set up the private gateway before installing its background service")
	}
	s, err := relayServiceSpecCurrent()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.Path), 0755); err != nil {
		return err
	}
	unit, err := os.OpenFile(s.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return errors.New("a private gateway service already exists; use ptln relay service restart to restart it")
		}
		return err
	}
	// Only remove the definition this call created. A failed registration must
	// not leave a file that prevents the user from retrying start.
	installed := false
	attempted := false
	defer func() {
		if !installed {
			if attempted {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if stopErr := runRelayServiceCommands(cleanup, s.Stop); stopErr != nil && !relayServiceNotLoaded(stopErr) {
					err = errors.Join(err, fmt.Errorf("service registration cleanup failed: %w", stopErr))
				}
			}
			if removeErr := os.Remove(s.Path); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, fmt.Errorf("could not remove new service definition %s: %w", s.Path, removeErr))
			}
		}
	}()
	_, writeErr := unit.WriteString(s.Contents)
	closeErr := unit.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	log, err := os.OpenFile(filepath.Join(relayHostDir(), "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	log.Close()
	attempted = true
	if err = runRelayServiceCommands(ctx, s.Start); err != nil {
		return fmt.Errorf("service registration failed; gateway settings kept; retry ptln relay service start: %w", err)
	}
	installed = true
	return nil
}
func relayServiceMain(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: ptln relay service start|stop|restart|status")
	}
	s, err := relayServiceSpecCurrent()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch args[0] {
	case "start":
		return relayServiceStart(ctx)
	case "stop":
		return stopRelayService(ctx, s)
	case "restart":
		if runtime.GOOS == "darwin" {
			return runRelayServiceCommands(ctx, [][]string{{"launchctl", "kickstart", "-k", "gui/" + strconv.Itoa(os.Getuid()) + "/" + relayServiceLabel}})
		}
		return runRelayServiceCommands(ctx, [][]string{{"systemctl", "--user", "restart", "partyline-relay.service"}})
	case "status":
		for _, command := range s.Status {
			cmd := exec.CommandContext(ctx, command[0], command[1:]...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err = cmd.Run(); err != nil {
				return err
			}
		}
		fmt.Println(s.Why)
		return nil
	default:
		return errors.New("usage: ptln relay service start|stop|restart|status")
	}
}
