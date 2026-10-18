# Contributing

Thanks for helping improve partyline! This repo is the open-source client — a single Go
binary (the CLI) plus the blind relay. The hosted web control plane is a separate service.

## Build & test

```sh
go build ./...      # build everything
go test ./...       # run the suite
go vet ./...        # static checks
gofmt -l .          # must be empty (run `gofmt -w` to fix)
```

Go version: see `go.mod`. The module path is `partyline.sh/partyline`.

## Pull requests

- Keep changes focused; match the surrounding style.
- `gofmt`, `go vet`, and `go test ./...` must pass.
- For anything touching crypto, the relay, or the join path, explain the security
  reasoning in the PR description.
- For security-sensitive reports, see [SECURITY.md](SECURITY.md) — don't open a public
  issue for exploitable bugs.
