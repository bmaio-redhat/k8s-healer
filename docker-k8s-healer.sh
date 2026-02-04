#!/bin/bash

# Wrapper script for running k8s-healer in Docker/Podman
# Build the image first: make docker-build  (or: docker build -t k8s-healer .)
# Automatically handles volume mounting for kubeconfig files
# Prefers Podman, falls back to Docker
# Usage: docker-k8s-healer.sh [docker|podman] [k8s-healer args...]
#        Or: ./docker-k8s-healer.sh -k /path/to/kubeconfig -n "pw-*"
# Daemon: ./docker-k8s-healer.sh -k /path/to/kubeconfig start --pid-file /tmp/healer.pid --log-file /tmp/healer.log

set -e

# Trap signals to ensure cleanup (though --rm should handle this)
trap 'exit 130' INT TERM

ARGS=("$@")

# Check if first arg is explicitly docker or podman
if [ "$1" = "docker" ] || [ "$1" = "podman" ]; then
    CONTAINER_CMD="$1"
    shift
    ARGS=("$@")
else
    # Auto-detect: prefer podman, fall back to docker
    if command -v podman >/dev/null 2>&1; then
        CONTAINER_CMD="podman"
    elif command -v docker >/dev/null 2>&1; then
        CONTAINER_CMD="docker"
    else
        echo "Error: Neither podman nor docker found in PATH" >&2
        exit 1
    fi
fi

IMAGE_NAME="k8s-healer"
CONTAINER_NAME="k8s-healer"
KUBECONFIG_PATH=""
KUBECONFIG_MOUNT="/root/.kube/config"

# Parse arguments to find kubeconfig path
i=0
while [ $i -lt ${#ARGS[@]} ]; do
    arg="${ARGS[$i]}"
    
    if [ "$arg" = "-k" ] || [ "$arg" = "--kubeconfig" ]; then
        # Next argument is the kubeconfig path
        if [ $((i+1)) -lt ${#ARGS[@]} ]; then
            KUBECONFIG_PATH="${ARGS[$((i+1))]}"
            # Convert to absolute path if relative
            if [[ "$KUBECONFIG_PATH" != /* ]]; then
                KUBECONFIG_PATH="$(cd "$(dirname "$KUBECONFIG_PATH")" && pwd)/$(basename "$KUBECONFIG_PATH")"
            fi
            # Check if file exists
            if [ ! -f "$KUBECONFIG_PATH" ]; then
                echo "Error: Kubeconfig file not found: $KUBECONFIG_PATH" >&2
                exit 1
            fi
            # Replace the path in args with the container mount path
            ARGS[$((i+1))]="$KUBECONFIG_MOUNT"
            break
        fi
    fi
    i=$((i+1))
done

# Build docker/podman command
CMD=("$CONTAINER_CMD" "run" "--rm")

# Check for daemon mode
DAEMON=false
for arg in "${ARGS[@]}"; do
    if [ "$arg" = "--daemon" ] || [ "$arg" = "-d" ]; then
        DAEMON=true
        break
    fi
done

if [ "$DAEMON" = true ]; then
    CMD+=("-d" "--name" "$CONTAINER_NAME")
fi

# Add volume mount for kubeconfig
# Use :z for SELinux context (Podman needs this, Docker ignores it)
if [ -n "$KUBECONFIG_PATH" ]; then
    CMD+=("-v" "${KUBECONFIG_PATH}:${KUBECONFIG_MOUNT}:ro,z")
else
    # Try default location
    DEFAULT_KUBECONFIG="${HOME}/.kube/config"
    if [ -f "$DEFAULT_KUBECONFIG" ]; then
        CMD+=("-v" "${DEFAULT_KUBECONFIG}:${KUBECONFIG_MOUNT}:ro,z")
    else
        echo "Error: No kubeconfig specified and default ~/.kube/config not found" >&2
        echo "Please specify kubeconfig with -k or --kubeconfig flag" >&2
        exit 1
    fi
fi

# Add image name and healer arguments
CMD+=("$IMAGE_NAME" "${ARGS[@]}")

# Execute the command
# Using exec ensures signals (like Ctrl+C) are forwarded to the container
# The --rm flag ensures the container is automatically removed when it exits
exec "${CMD[@]}"
