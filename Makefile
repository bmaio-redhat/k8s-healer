.PHONY: test test-coverage test-verbose clean build

# Run all tests
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

# Clean build artifacts
clean:
	rm -f k8s-healer coverage.out coverage.html

# Run tests for specific package
test-util:
	go test -v ./pkg/util/...

test-daemon:
	go test -v ./pkg/daemon/...

test-healer:
	go test -v ./pkg/healer/...
