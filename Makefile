# The gates, in one place, so a contributor and CI run the same commands.
#
# Until now there was no Makefile and no CI gate on pull requests, which meant "the checks" were
# whatever the last person remembered to type. Both halves of that are fixed: .github/workflows/ci.yml
# runs these on every pull request, and this file is what a human runs before opening one.

.PHONY: check check-go check-web surface-gen surface-check docs-audit llms-check fmt test build help

help:
	@echo "make check          every gate CI runs (go + web)"
	@echo "make check-go       gofmt, build, vet, test, surface drift"
	@echo "make check-web      tsc, vitest, next build"
	@echo "make surface-gen    regenerate the derived reference + llms.txt + TS vocab"
	@echo "make surface-check  fail if any generated artifact was hand-edited or is stale"
	@echo "make docs-audit     diff the extracted surface against what the docs CLAIM to cover"
	@echo "make llms-check     prove partyline.sh/llms-full.txt is served as plain text, byte-identical"
	@echo "                    (BASE=--local starts the local build instead of hitting production)"

check: check-go check-web

# gofmt -l prints offenders and exits 0, so emptiness IS the assertion — hence the explicit test.
check-go:
	@unformatted=$$(gofmt -l . | grep -v '^$$' || true); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
	go build ./...
	go vet ./...
	go test ./...
	# The fleet runs on Linux. A darwin-only symbol referenced from an untagged file compiles
	# fine here and breaks there — which is exactly how the root package sat un-buildable on
	# Linux until CI first vetted it. Cross-vet catches that on a laptop, before the push.
	GOOS=linux GOARCH=amd64 go vet ./...
	go run ./cmd/gc-surface -check

# `npm run build` is NOT optional and must not be dropped for speed: tsc cannot see the Next
# server/client boundary, and a violation there broke every deploy from PR #330 to #335 (#142).
check-web:
	cd web && npx tsc --noEmit
	cd web && npx vitest run
	cd web && npm run build

# Epic D — the docs gap check. Prints a worklist, not a verdict: every undocumented item is a
# bounded task whose acceptance criterion is its own gap going to zero, which is the task shape that
# produces mergeable autonomous work. PHANTOMS (a doc naming code that does not exist) are the line
# to actually care about — those are actively misleading rather than merely missing.
docs-audit:
	@go run ./cmd/gc-surface -audit

surface-gen:
	go run ./cmd/gc-surface -gen

surface-check:
	go run ./cmd/gc-surface -check

# The disk-level gates above cannot see an auth gate, a redirect, a wrong content type, or a box
# still serving a stale copy. This one does, because it speaks HTTP. Not part of `make check`: it
# needs a running site. `BASE=--local` starts the build in web/ and checks that; a URL checks a
# deployment (default: production). CI runs the --local form after `next build`.
llms-check:
	@scripts/check-llms-full.sh $(BASE)

fmt:
	gofmt -w .

test:
	go test ./...
	cd web && npx vitest run

build:
	go build -o bin/ptln .
