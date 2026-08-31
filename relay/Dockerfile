# partyline relay — static Go binary on alpine (needs ca-certificates for the
# outbound control-plane ownership-verification call).
#
# CROSS-COMPILED, NOT EMULATED. The build stage is pinned to $BUILDPLATFORM — the architecture of
# whatever is running the build — and the Go toolchain is told which architecture to EMIT via
# $TARGETARCH. That makes the linux/arm64 image cost the same as the linux/amd64 one, because Go
# with CGO_ENABLED=0 cross-compiles natively and nothing foreign ever has to execute.
#
# Without the `--platform=$BUILDPLATFORM` pin, buildx would run the whole golang stage under QEMU
# for the arm64 leg — the same compile, several times slower, for an identical binary.
#
# The emitted binary is byte-for-byte what the old `go build` produced for amd64: same source, same
# CGO_ENABLED=0, same flags. GOARCH is simply explicit now instead of implied by the host.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -o /relay ./relay

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /relay /relay
ENV PORT=2222
EXPOSE 2222
CMD ["/relay"]
