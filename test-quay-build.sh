#!/bin/bash
# Local test script for Quay.io build and push (k8s-healer)
# Simulates a CI pipeline: build image, tag, push to Quay

set -e

# Script and project root (same directory as this script)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR"
cd "$PROJECT_ROOT"

# Configuration - all must be set via environment variables
# Required:
#   QUAY_REGISTRY   - Registry URL (e.g., quay.io)
#   QUAY_USERNAME   - Robot account username (e.g., org+robot_name)
#   QUAY_PASSWORD   - Robot account password
#   QUAY_REPOSITORY - Repository path (e.g., org/k8s-healer)
# Optional:
#   CI_COMMIT_BRANCH    - Git branch (defaults to current branch)
#   CI_COMMIT_TAG       - Git tag (if building from a tag)
#   CI_COMMIT_SHORT_SHA - Commit SHA (defaults to current short SHA)
#   CI_COMMIT_REF_SLUG  - Branch slug (defaults to CI_COMMIT_BRANCH)

# Validate required environment variables
if [ -z "$QUAY_REGISTRY" ]; then
    echo "ERROR: QUAY_REGISTRY not set. Export it before running."
    exit 1
fi
if [ -z "$QUAY_USERNAME" ]; then
    echo "ERROR: QUAY_USERNAME not set. Export it before running."
    exit 1
fi
if [ -z "$QUAY_PASSWORD" ]; then
    echo "ERROR: QUAY_PASSWORD not set. Export it before running."
    exit 1
fi
if [ -z "$QUAY_REPOSITORY" ]; then
    echo "ERROR: QUAY_REPOSITORY not set. Export it before running."
    exit 1
fi

# Optional CI-like variables (default from git)
CI_COMMIT_BRANCH="${CI_COMMIT_BRANCH:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '')}"
CI_COMMIT_TAG="${CI_COMMIT_TAG:-}"
CI_COMMIT_SHORT_SHA="${CI_COMMIT_SHORT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')}"
CI_COMMIT_REF_SLUG="${CI_COMMIT_REF_SLUG:-${CI_COMMIT_BRANCH}}"

echo "=== Testing Quay.io Build and Push (k8s-healer) ==="
echo "Registry:   $QUAY_REGISTRY"
echo "Repository: $QUAY_REPOSITORY"
echo "Branch:     $CI_COMMIT_BRANCH"
echo "Commit SHA: $CI_COMMIT_SHORT_SHA"
echo ""

# Prerequisites
if ! command -v podman &> /dev/null; then
    echo "ERROR: podman not found. Install podman or use docker and adapt this script."
    exit 1
fi

# Step 1: Setup
echo "Step 1: Checking podman..."
podman --version
echo "✓ Ready"
echo ""

# Step 2: Login to Quay
echo "Step 2: Logging in to Quay..."
echo "$QUAY_PASSWORD" | podman login -u "$QUAY_USERNAME" --password-stdin "$QUAY_REGISTRY"
echo "✓ Login successful"
echo ""

# Step 3: Determine image tag
echo "Step 3: Determining image tag..."
if [ -n "$CI_COMMIT_TAG" ]; then
    export IMAGE_TAG="$CI_COMMIT_TAG"
elif [ -n "$CI_COMMIT_REF_SLUG" ]; then
    export IMAGE_TAG="${CI_COMMIT_REF_SLUG}-${CI_COMMIT_SHORT_SHA}"
else
    export IMAGE_TAG="${CI_COMMIT_SHORT_SHA}"
fi
echo "Image tag: $IMAGE_TAG"
echo ""

# Step 4: Build the image (Dockerfile at project root)
echo "Step 4: Building k8s-healer image..."
echo "Building: $QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG"
podman build -t "$QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG" -f Dockerfile .
echo "✓ Build successful"
echo ""

# Step 5: Tag as latest (unless IMAGE_TAG is already latest)
if [ "$IMAGE_TAG" != "latest" ]; then
    echo "Step 5: Tagging as latest..."
    podman tag "$QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG" "$QUAY_REGISTRY/$QUAY_REPOSITORY:latest"
    echo "✓ Tagged as latest"
    echo ""
fi

# Step 6: Tag as branch name when on a named branch (e.g. main, main-abc1234)
if [ -n "$CI_COMMIT_BRANCH" ] && [ "$CI_COMMIT_BRANCH" != "HEAD" ] && [ -z "$CI_COMMIT_TAG" ]; then
    echo "Step 6: Tagging as branch '$CI_COMMIT_BRANCH'..."
    podman tag "$QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG" "$QUAY_REGISTRY/$QUAY_REPOSITORY:$CI_COMMIT_BRANCH"
    echo "✓ Tagged as $CI_COMMIT_BRANCH"
    echo ""
fi

# Step 7: Push primary tag
echo "Step 7: Pushing image to Quay..."
podman push "$QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG"
echo "✓ Pushed: $QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG"
echo ""

# Step 8: Push latest
if [ "$IMAGE_TAG" != "latest" ]; then
    echo "Step 8: Pushing latest tag..."
    podman push "$QUAY_REGISTRY/$QUAY_REPOSITORY:latest"
    echo "✓ Pushed: $QUAY_REGISTRY/$QUAY_REPOSITORY:latest"
    echo ""
fi

# Step 9: Push branch tag if applicable
if [ -n "$CI_COMMIT_BRANCH" ] && [ "$CI_COMMIT_BRANCH" != "HEAD" ] && [ -z "$CI_COMMIT_TAG" ]; then
    echo "Step 9: Pushing branch tag $CI_COMMIT_BRANCH..."
    podman push "$QUAY_REGISTRY/$QUAY_REPOSITORY:$CI_COMMIT_BRANCH"
    echo "✓ Pushed: $QUAY_REGISTRY/$QUAY_REPOSITORY:$CI_COMMIT_BRANCH"
    echo ""
fi

# Step 10: Logout
echo "Step 10: Logging out..."
podman logout "$QUAY_REGISTRY"
echo "✓ Logout successful"
echo ""

echo "=== Success! ==="
echo "Image: $QUAY_REGISTRY/$QUAY_REPOSITORY:$IMAGE_TAG"
[ "$IMAGE_TAG" != "latest" ] && echo "Also: $QUAY_REGISTRY/$QUAY_REPOSITORY:latest"
[ -n "$CI_COMMIT_BRANCH" ] && [ "$CI_COMMIT_BRANCH" != "HEAD" ] && [ -z "$CI_COMMIT_TAG" ] && echo "Also: $QUAY_REGISTRY/$QUAY_REPOSITORY:$CI_COMMIT_BRANCH"
