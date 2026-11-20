.PHONY: test lint fmt vet integration clean check security check-all clean-all doctor

# Run unit tests (short mode, no integration)
test:
	go test -v -short ./...

# Run tests with coverage
test-cov:
	go test -v -short -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

# Run integration tests (requires CUBRID)
integration:
	go test -v -run Integration ./...

# Run go vet
vet:
	go vet ./...

# Run golangci-lint (if installed)
lint: vet
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

# Format code
fmt:
	gofmt -w .

# Clean build artifacts
clean:
	rm -f coverage.out

# Full check
check: fmt vet test

# Security scan
security:
	@which govulncheck > /dev/null 2>&1 && govulncheck ./... || echo "govulncheck not installed, skipping"

# Full check (all quality gates)
check-all: fmt vet lint security test

# Clean all artifacts
clean-all: clean
	rm -rf dist/ bin/

# Doctor check (verify tool availability)
doctor:
	@echo "=== Go Environment ==="
	@go version
	@echo ""
	@echo "=== Tools ==="
	@which golangci-lint > /dev/null 2>&1 && echo "✓ golangci-lint" || echo "✗ golangci-lint (optional)"
	@which govulncheck > /dev/null 2>&1 && echo "✓ govulncheck" || echo "✗ govulncheck (optional: go install golang.org/x/vuln/cmd/govulncheck@latest)"
