.PHONY: test test-coverage test-verbose clean build docker-build test-list

# Run all tests (every package under pkg/: daemon, discovery, healer, healthcheck, util; includes all *_test.go files)
test:
	go test ./pkg/...

# Run tests with verbose output
test-verbose:
	go test -v ./pkg/...

# Run tests with coverage
test-coverage:
	go test -coverprofile=coverage.out ./pkg/...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Show coverage summary
test-coverage-summary:
	go test -coverprofile=coverage.out ./pkg/...
	go tool cover -func=coverage.out

# Build the binary
build:
	go build -o k8s-healer ./cmd/main.go

# Build Docker/Podman image (image name must match docker-k8s-healer.sh)
docker-build:
	docker build -t k8s-healer .

# Clean build artifacts
clean:
	rm -f k8s-healer coverage.out coverage.html

# List packages that are tested (for verification)
test-list:
	@echo "Test packages (go test ./pkg/...):"
	@go list ./pkg/...

# Run tests for specific package
test-util:
	go test -v ./pkg/util/...

test-daemon:
	go test -v ./pkg/daemon/...

test-healer:
	go test -v ./pkg/healer/...

test-healthcheck:
	go test -v ./pkg/healthcheck/...

test-discovery:
	go test -v ./pkg/discovery/...
