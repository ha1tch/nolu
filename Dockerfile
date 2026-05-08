# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor/ vendor/

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu        ./cmd/nolu
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-proxy   ./cmd/nolu-proxy
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo  ./cmd/demo
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo1 ./cmd/demo1
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo2 ./cmd/demo2
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo3 ./cmd/demo3
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo4 ./cmd/demo4
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo5 ./cmd/demo5
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o /bin/nolu-demo6 ./cmd/demo6

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /bin/nolu        /usr/local/bin/nolu
COPY --from=builder /bin/nolu-proxy   /usr/local/bin/nolu-proxy
COPY --from=builder /bin/nolu-demo  /usr/local/bin/nolu-demo
COPY --from=builder /bin/nolu-demo1 /usr/local/bin/nolu-demo1
COPY --from=builder /bin/nolu-demo2 /usr/local/bin/nolu-demo2
COPY --from=builder /bin/nolu-demo3 /usr/local/bin/nolu-demo3
COPY --from=builder /bin/nolu-demo4 /usr/local/bin/nolu-demo4
COPY --from=builder /bin/nolu-demo5 /usr/local/bin/nolu-demo5
COPY --from=builder /bin/nolu-demo6 /usr/local/bin/nolu-demo6

EXPOSE 7070

ENTRYPOINT ["/usr/local/bin/nolu"]
