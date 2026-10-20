# syntax=docker/dockerfile:1
#
# PORTABILITY: nothing environment-specific lives here — no internal artifact
# mirror, no proxy, no regional package mirror, no *.svc.cluster.local. A CI
# runner and a developer laptop build the same image.
FROM golang:1.26-alpine AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/sandbox-ext ./cmd/sandbox-ext

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/sandbox-ext /app/sandbox-ext
EXPOSE 8080
ENTRYPOINT ["/app/sandbox-ext"]
