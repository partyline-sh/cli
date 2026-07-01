# partyline relay — static Go binary on alpine (needs ca-certificates for the
# outbound control-plane ownership-verification call).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /relay ./relay

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /relay /relay
ENV PORT=2222
EXPOSE 2222
CMD ["/relay"]
