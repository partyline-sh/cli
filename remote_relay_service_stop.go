package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

type relayServiceCommandError struct {
	Command, Output string
	Err             error
}

func (e *relayServiceCommandError) Error() string {
	return fmt.Sprintf("%s: %v: %s", e.Command, e.Err, e.Output)
}
func (e *relayServiceCommandError) Unwrap() error { return e.Err }

// Match the manager's explicit missing-service diagnostics. Missing user buses,
// permission errors and other failures must not be mistaken for an unloaded job.
// Commands run with LC_ALL=C so these diagnostics are independent of locale.
func relayServiceNotLoaded(err error) bool {
	var command *relayServiceCommandError
	if !errors.As(err, &command) {
		return false
	}
	output := strings.ToLower(command.Output)
	for _, failure := range []string{"failed to connect to bus", "permission denied", "access denied", "operation not permitted"} {
		if strings.Contains(output, failure) {
			return false
		}
	}
	switch command.Command {
	case "launchctl":
		for _, missing := range []string{"no such process", "could not find service", "could not find specified service"} {
			if strings.Contains(output, missing) {
				return true
			}
		}
	case "systemctl":
		if !strings.Contains(output, "partyline-relay.service") {
			return false
		}
		for _, missing := range []string{"not loaded", "not found", "does not exist"} {
			if strings.Contains(output, missing) {
				return true
			}
		}
	}
	return false
}

func stopRelayService(ctx context.Context, s relayServiceSpec) error {
	if err := runRelayServiceCommands(ctx, s.Stop); err != nil && !relayServiceNotLoaded(err) {
		return err
	}
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if runtime.GOOS == "linux" {
		return runRelayServiceCommands(ctx, [][]string{{"systemctl", "--user", "daemon-reload"}})
	}
	return nil
}
