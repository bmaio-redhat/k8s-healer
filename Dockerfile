# Build stage
# Pin Go version to match go.mod (requires go >= 1.24.7)
FROM golang:1.24-alpine AS builder

# Install git and ca-certificates (needed for some Go modules and SSL)
RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Copy dependency manifests first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source (cmd + pkg only; .dockerignore excludes tests, docs, scripts)
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/

# Build static binary; TARGETOS/TARGETARCH allow multi-arch builds (e.g. quay.io, buildx)
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s" \
    -o k8s-healer \
    ./cmd/main.go

# Final stage: minimal image for quay.io / Kubernetes
FROM scratch

# OCI labels for Quay and registries (optional but recommended)
LABEL org.opencontainers.image.source="https://github.com/bmaio-redhat/k8s-healer" \
    org.opencontainers.image.title="k8s-healer" \
    org.opencontainers.image.description="Kubernetes pod/VM/CRD healer and test-environment janitor"

COPY --from=builder /build/k8s-healer /k8s-healer
ENTRYPOINT ["/k8s-healer"]
