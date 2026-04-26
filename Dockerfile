# ─── Stage 1: Build ───────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

# Install build essentials (CGO disabled for static binary)
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /aegis-server .

# ─── Stage 2: Runtime ─────────────────────────────────────────────────────────
FROM scratch

# Copy CA certs (needed for TLS / AWS SDK calls)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Copy timezone data (needed for time.LoadLocation)
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
# Copy the compiled binary
COPY --from=builder /aegis-server /aegis-server
# Copy the default config (users can mount their own via -v)
COPY --from=builder /app/config.yaml /config.yaml

# Expose HTTP and gRPC ports
EXPOSE 8080 50051

ENTRYPOINT ["/aegis-server"]
