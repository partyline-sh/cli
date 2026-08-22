//go:build !darwin

package main

import "fmt"

// The menu bar companion is macOS-only (GNOME dropped legacy tray support, so a Linux icon may
// simply never appear). `ptln tray` says so plainly instead of the generic unknown-subcommand error.
func trayMain(_ []string) {
	fmt.Println("The tray icon is macOS-only for now.")
	fmt.Println("  Linux desktops dropped legacy tray support, so the icon can't be relied on there.")
}
