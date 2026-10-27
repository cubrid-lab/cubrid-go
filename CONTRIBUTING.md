# Contributing to cubrid-go

**Issue titles:** use a type-only prefix such as `feat:` or `bug:`, without a module scope. **PR and commit titles:** continue to use the existing Conventional Commits format and local validators. See the [shared policy](https://github.com/cubrid-lab/.github/blob/main/docs/ISSUE_PR_COMMIT_STANDARD.md) for evidence, acceptance criteria and triage responsibilities.

Thank you for your interest in contributing! This document provides guidelines
and instructions for contributing to the project.

## Table of Contents

- [Development Setup](#development-setup)
- [Running Tests](#running-tests)
- [Code Style](#code-style)
- [Pull Request Guidelines](#pull-request-guidelines)
- [Reporting Issues](#reporting-issues)

---

## Development Setup

### Prerequisites

- Go 1.21 or later
- Git
- Docker (for integration tests)

### Installation

```bash
# Clone the repository
git clone https://github.com/cubrid-lab/cubrid-go.git
cd cubrid-go

# Download dependencies
go mod download

# Verify
go vet ./...
```

---

## Running Tests

### Unit Tests

```bash
# Run tests (excluding integration)
go test -v -short ./...

# Run tests with coverage
go test -v -short -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

### Integration Tests (Requires CUBRID)

```bash
# Start a CUBRID container
docker compose up -d

# Set the connection URL
export CUBRID_TEST_DSN="CUBRID:localhost:33000:testdb:::"

# Run integration tests
go test -v -run Integration ./...

# Stop the container when done
docker compose down
```

---

## Code Style

This project follows standard Go conventions.

### Tools

- **Formatter**: `go fmt` / `gofmt`
- **Linter**: `go vet`
- **Static analysis**: [golangci-lint](https://golangci-lint.run/) (recommended)

### Running Checks

```bash
# Format
gofmt -w .

# Vet
go vet ./...

# Lint (if golangci-lint is installed)
golangci-lint run
```

### Conventions

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use meaningful variable and function names
- Write godoc comments for exported types and functions
- Keep functions small and focused

---

## Pull Request Review and Merge Policy

All contributors follow the same process regardless of whether they use
OpenCode, Codex, Claude Code, ChatGPT, Copilot or no AI tool. Keep a PR
focused, provide relevant tests and affected docs, and describe validations
not performed locally. **Required GitHub Actions CI** is the authoritative
test record for the final head; local runs are recommended but not an extra
merge gate. Oracle, Copilot and `agent-review` may help reviewers but their
results are advisory, not required tool-specific approvals. The responsible
maintainer checks correctness, tests, security, maintainability, CI and
outstanding review findings before merging. Changes affecting credentials,
workflow write permissions or release/publishing operations receive explicit
independent maintainer scrutiny. Existing branch protection and repository
test commands remain unchanged.

## Pull Request Guidelines

### Before Submitting

1. **Create a feature branch** from `main`:
   ```bash
   git checkout -b feature/my-feature main
   ```

2. **Write tests** for any new functionality.

3. **Run the full test suite** and ensure all tests pass:
   ```bash
   make test
   ```

4. **Run lint checks**:
   ```bash
   make lint
   ```

### PR Content

- Keep PRs focused — one feature or fix per PR.
- Write a clear title and description explaining _what_ and _why_.
- Reference any related issues (e.g., `Fixes #42`).
- Update documentation if your change affects the public API.
- Update `CHANGELOG.md` with a summary of your change.

### Review Process

- All PRs require at least one review before merge.
- CI must pass (vet, tests).
- Maintain backward compatibility unless explicitly approved.

---

## Repository-local issue and agent handoff

Before a change, review the current Go driver behavior, this repository's
open issues and overlapping PRs. A new issue enters `status: needs triage`;
maintainers validate evidence, duplicates, scope, type and relative priority
against the entire local backlog. Confirm one
`priority: critical/high/medium/low` and one `size: XS/S/M/L/XL`, then
read the saved labels before clearing the triage status. Priority does not
override blocked prerequisites or required final-head CI.

A human contributor may propose or implement unclaimed work without a
mandatory AI reviewer or pre-approval. Maintainer-operated autonomous agents
need specific authorization and an actual issue assignee for reserved work.
Check PR ownership and coordinate before changing the same behavior.

For request cancellation, prepared handles, reconnection or transaction
changes, preserve `database/sql` semantics and include focused regression
tests. `make test` is an offline/short check; only the CUBRID-backed
`make integration` or the corresponding actual CI lane proves a real broker
round trip. `make lint` skips optional golangci-lint when absent: report
`go vet` success separately instead of claiming every optional linter ran.

## Reporting Issues

When reporting a bug, please include:

- Go version (`go version`)
- CUBRID server version
- cubrid-go version
- Minimal reproduction code
- Full error output

For feature requests, describe the use case and expected behavior.

---

## Questions?

Open a [GitHub Discussion](https://github.com/cubrid-lab/cubrid-go/discussions)
or file an [issue](https://github.com/cubrid-lab/cubrid-go/issues).
