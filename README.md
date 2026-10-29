[![Continuous Integration](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/ci.yml) [![CodeQL Advanced](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/codeql.yml/badge.svg)](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/codeql.yml) [![Vulnerability Scan](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/vulnerability-scan.yml/badge.svg?branch=main)](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/vulnerability-scan.yml) [![OSS Compliance Validation](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/oss-compliance.yml/badge.svg?branch=main)](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/actions/workflows/oss-compliance.yml) [![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/autonomous-operations-platform/autonomous-operations-satellite/badge)](https://scorecard.dev/viewer/?uri=github.com/autonomous-operations-platform/autonomous-operations-satellite) [![REUSE status](https://api.reuse.software/badge/github.com/autonomous-operations-platform/autonomous-operations-satellite)](https://api.reuse.software/info/github.com/autonomous-operations-platform/autonomous-operations-satellite)

# Autonomous Operations Satellite Framework
The Satellite is the per-node service in the Autonomous Operations stack. It gathers data from data center infrastructure (metrics, logs, traces) via **collectors**, persists it to [Apache Doris](https://doris.apache.org/), and drives a pipeline of **observers** (pattern detectors that emit insights) and **operators** (that react to insights with remediation actions).
For more details, check out [software design documentation on the Autonomous Operations Satellite](https://pages.github.com/autonomous-operations-platform/autonomous-operations/#/architecture/software-design/autonomous-operations/autonomous-operations-satellite).

## About this project

Executes deterministic, rule-based data center automation on bare metal. Operates independently of centralized services, including during network partitions and control-plane outages.

## Building a Satellite

New to the framework? The [Getting Started guide](https://github.com/autonomous-operations-platform/hello-world-satellite/blob/main/GETTING-STARTED.md)
in `hello-world-satellite` walks you through building a working Satellite by hand.

A Satellite orchestrates three kinds of components:

- A **Collector** gathers data and writes telemetry.
- An **Observer** reads telemetry, detects a pattern, and writes an insight.
- An **Operator** reads an insight and performs a remediation action.

Components self-register in their package `init()`. The `main` file blank-imports those
packages and calls `satellite.Run`, which starts everything in the right order.

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

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/autonomous-operations-platform/autonomous-operations-satellite/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/autonomous-operations-platform/.github/blob/main/CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/autonomous-operations-platform/.github/blob/main/SECURITY.md) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/autonomous-operations-platform/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2026 SAP SE or an SAP affiliate company and Autonomous Operations Platform contributors. Please see our [LICENSE](./LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/autonomous-operations-platform/autonomous-operations-satellite).


- **`aos-framework`** The Collector, Observer, and Operator interfaces, the store, and the `satellite.Run` orchestrator.
- **[`hello-world-satellite`](https://github.com/autonomous-operations-platform/hello-world-satellite)** — a demo satellite built on this framework. Start with its [Getting Started guide](https://github.com/autonomous-operations-platform/hello-world-satellite/blob/main/GETTING-STARTED.md).
