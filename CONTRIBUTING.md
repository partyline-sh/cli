# Contributing

This repository is the generated public source snapshot of Partyline: a Go CLI,
a blind relay and the native mobile source included in the release. There is no
hosted Partyline control plane.

## Build and checks

The Go version and module path are in `go.mod`.

```sh
go build -o ptln .
go vet ./...
gofmt -l .
```

Native app development instructions are in `mobile/README.md`. The complete
release gate runs in the development repository; the public mirror omits the
website, tooling and private development documentation. Tests that require those
fixtures need the full development checkout.

## Changes and reports

The mirror is replaced by a generated snapshot after a release. Open an issue to
report a bug or discuss a patch before sending changes to this generated history.
Keep patches focused, follow the surrounding style, and explain the security
reasoning for changes to encryption, relays, pairing or authorization.

For exploitable issues, follow [SECURITY.md](SECURITY.md) and report privately.
Do not disclose a vulnerability in a public issue.
