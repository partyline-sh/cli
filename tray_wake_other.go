//go:build !darwin

package main

// The menu bar companion is macOS-only, so there's nothing to wake elsewhere.
func wakeTray() {}
