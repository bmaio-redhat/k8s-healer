# Build stage
# Using latest alpine for Go (will use latest stable Go version)
FROM golang:alpine AS builder

# Install git and ca-certificates (needed for some Go modules and SSL)
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/

# Build static binary
# CGO_ENABLED=0 creates a static binary that works in scratch
# -ldflags="-w -s" strips debug info to reduce binary size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o k8s-healer \
    ./cmd/main.go

# Final stage - use scratch for smallest image
FROM scratch

# Copy CA certificates from builder (needed for HTTPS connections to Kubernetes API)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary
COPY --from=builder /build/k8s-healer /k8s-healer

# Set the binary as entrypoint
ENTRYPOINT ["/k8s-healer"]
