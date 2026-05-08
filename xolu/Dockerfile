# syntax=docker/dockerfile:1

# ---- build stage ----
# Always build on the native runner platform; use Go cross-compilation
# to target the requested architecture. This avoids QEMU emulation for
# the compile step, which can be 10-20x slower on arm64.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETARCH

# modernc.org/sqlite is pure Go; no CGO or sqlite-dev headers required.
ENV GONOSUMDB=*
ENV GOFLAGS=-mod=mod
ENV CGO_ENABLED=0
ENV GOOS=linux

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" \
    -o /out/olu ./cmd/olu

# ---- runtime stage ----
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 1001 olu

# Data directory — mount a volume here for persistent storage.
RUN mkdir -p /data && chown olu:olu /data

COPY --from=builder /out/olu /usr/local/bin/olu

USER olu
WORKDIR /data

# ---- configuration via environment variables ----
#
# Primary variables (olu's canonical names):
#   OLU_HOST              Listen host              (default: 0.0.0.0)
#   OLU_PORT              Listen port              (default: 9090)
#   OLU_STORAGE_TYPE      Storage backend          (sqlite|jsonfile, default: sqlite)
#   OLU_DB_PATH           SQLite database path     (default: olu.db)
#   OLU_AUTH_TYPE         Auth mechanism           (none|jwt|apikey|bearertoken)
#   OLU_INTERNAL_TOKEN    Bearer token secret      (or /run/secrets/olu_internal_token)
#   OLU_JWT_SECRET        JWT HMAC secret          (or /run/secrets/olu_jwt_secret)
#   OLU_LOG_LEVEL         Log level                (debug|info|warn|error, default: info)
#   OLU_METRICS_ENABLED   Enable Prometheus metrics (default: false)
#   OLU_METRICS_HOST      Metrics listen host      (default: 0.0.0.0)
#   OLU_METRICS_PORT      Metrics listen port      (default: 9091)
#
# Convenience aliases:
#   OLU_ADDR              host:port  →  OLU_HOST + OLU_PORT
#   OLU_METRICS_ADDR      host:port  →  OLU_METRICS_HOST + OLU_METRICS_PORT
#   OLU_SQLITE_PATH       path       →  OLU_DB_PATH
#
# Docker secret mounts (used when the corresponding env var is not set):
#   /run/secrets/olu_internal_token
#   /run/secrets/olu_jwt_secret

EXPOSE 9090

ENTRYPOINT ["/usr/local/bin/olu"]
