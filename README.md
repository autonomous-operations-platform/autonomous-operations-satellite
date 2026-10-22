# AOS Satellite Framework

The Satellite is the per-node service in the Autonomous Operations stack. It gathers data from data center infrastructure (metrics, logs, traces) via **collectors**, persists it to [Apache Doris](https://doris.apache.org/), and drives a pipeline of **observers** (pattern detectors that emit insights) and **operators** (that react to insights with remediation actions).
For more details, check out [software design documentation on the Autonomous Operations Satellite](https://pages.github.com/autonomous-operations-platform/autonomous-operations/#/architecture/software-design/autonomous-operations/autonomous-operations-satellite).

## Where things live
Autonomous Operations spans three repositories:

- **`aos-framework`** The Collector, Observer, and Operator interfaces, the store, and the `satellite.Run` orchestrator.
- **[`hello-world-satellite`](https://github.com/autonomous-operations-platform/hello-world-satellite)** — a demo satellite built on this framework. Start with its [Getting Started guide](https://github.com/autonomous-operations-platform/hello-world-satellite/blob/main/GETTING-STARTED.md).

## Development setup

Install the required tools once after cloning:

```bash
# Import sorter (ensure $GOPATH/bin is in your PATH)
go install golang.org/x/tools/cmd/goimports@latest

# Linter
curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.14.0

# Pre-commit hooks (requires Python — https://pre-commit.com/#install)
pipx install pre-commit  # or: pip install pre-commit --break-system-packages
pre-commit install --hook-type pre-commit --hook-type commit-msg
```

## Formatting and linting

```bash
# Format all packages
go fmt ./...

# Sort imports
goimports -w .

# Lint
golangci-lint run ./...
```

`golangci-lint` also runs automatically as a pre-commit hook once the hooks are installed.

## Running tests

```bash
# All tests
go test ./...

# With race detector (recommended — the pub/sub path is heavily concurrent)
go test -race ./...

# A specific package
go test ./pkg/framework/operator/...

# HTML coverage report
go test -coverprofile=cover.out ./...
go tool cover -html=cover.out

# Quick per-package summary in the terminal
go test -cover ./...
```

### End-to-end tests (Doris)

The Doris e2e tests are gated behind the `e2e` build tag. They require Docker — the test harness starts a Doris container, waits for it to become ready (~90 s), runs the tests, and removes the container on exit.

```bash
go test -tags=e2e -v -timeout=5m ./pkg/store/doris/...
```

### Combined unit and e2e coverage

Run each suite separately, then merge the text profiles:

```bash
go test -coverprofile=unit.out ./...
go test -tags=e2e -timeout=5m -coverprofile=e2e.out ./pkg/store/doris/...

# Merge profiles
{ echo "mode: set"; grep -v "^mode:" unit.out; grep -v "^mode:" e2e.out; } > merged.out

# View report
go tool cover -html=merged.out
```

## Building a Satellite

New to the framework? The [Getting Started guide](https://github.com/autonomous-operations-platform/hello-world-satellite/blob/main/GETTING-STARTED.md)
in `hello-world-satellite` walks you through building a working Satellite by hand.

A Satellite orchestrates three kinds of components:

- A **Collector** gathers data and writes telemetry.
- An **Observer** reads telemetry, detects a pattern, and writes an insight.
- An **Operator** reads an insight and performs a remediation action.

Components self-register in their package `init()`. The `main` file blank-imports those
packages and calls `satellite.Run`, which starts everything in the right order.
