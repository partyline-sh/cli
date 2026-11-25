//go:build windows

package main

import "errors"

func desktopConnectMain(_ []string) {
	fatal(errors.New("ptln connect currently supports macOS and Linux"))
}
