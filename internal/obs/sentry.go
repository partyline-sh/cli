// Package obs: optional error reporting via Sentry. OPT-IN — only active when
// SENTRY_DSN is set in the environment. The CLI runs on users' machines, so we
// never phone home by default; our own infra (relay, CI builds) sets the DSN.
// Never send session content, keystrokes, tokens, or repo data — only crashes.
package obs

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

var enabled bool

// Init starts Sentry if SENTRY_DSN is set. Returns a flush func to defer.
func Init(component string) func() {
	dsn := strings.TrimSpace(os.Getenv("SENTRY_DSN"))
	if dsn == "" {
		return func() {}
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      envOr("PARTYLINE_ENV", "production"),
		Release:          os.Getenv("PARTYLINE_RELEASE"),
		AttachStacktrace: true,
		// privacy: don't let the SDK collect request bodies / PII
		SendDefaultPII: false,
	})
	if err != nil {
		return func() {}
	}
	enabled = true
	sentry.ConfigureScope(func(s *sentry.Scope) { s.SetTag("component", component) })
	return func() { sentry.Flush(2 * time.Second) }
}

// Recover captures a panic to Sentry (if enabled) then re-panics so existing
// behavior (crash + stderr) is preserved. Use as: defer obs.Recover().
func Recover() {
	if r := recover(); r != nil {
		if enabled {
			sentry.CurrentHub().Recover(r)
			sentry.Flush(2 * time.Second)
		}
		panic(r)
	}
}

// Guard recovers a panic in a BACKGROUND goroutine: report it (Sentry if enabled),
// log to stderr, and CONTINUE — so one bad joiner/stream/handler can't crash the
// whole host process. Use as: defer obs.Guard("serveJoiner").
func Guard(label string) {
	if r := recover(); r != nil {
		if enabled {
			sentry.CurrentHub().Recover(r)
			sentry.Flush(2 * time.Second)
		}
		fmt.Fprintf(os.Stderr, "\r\n[partyline] recovered panic in %s: %v\r\n", label, r)
	}
}

// CaptureError reports a non-fatal error (no-op if disabled).
func CaptureError(err error) {
	if enabled && err != nil {
		sentry.CaptureException(err)
	}
}

// Flush drains buffered events. Call before os.Exit, which skips deferred flushes.
func Flush() {
	if enabled {
		sentry.Flush(2 * time.Second)
	}
}

func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
