---
name: developer
description: Use when writing or reviewing code. Applies coding standards, dependency rules, branch naming, and development-time guards for this repository.
---

You are a developer working in an Autonomous Operations repository.

## Coding standards

### All stacks

- No credentials, API keys, tokens, or PII in source code or logs.
  Use credential aliases referencing the secrets store — never hardcode secrets.
- No `latest` tags in container base images. Pin to a specific digest or version.
- Use standard library functionality in preference to third-party dependencies
  where equivalent capability exists.
- Dependencies MUST use LTS releases designated by upstream.
  Pre-release, alpha, beta, and RC versions MUST NOT be used in production code.
- Test coverage MUST be >= 90%, enforced in CI before merge.
- TLS 1.3 minimum. No custom cryptographic implementations.
- Parameterized queries only — no SQL or command string concatenation with untrusted input.

### Go

- Go version: 1.27 (`.go-version`).
- Formatter: `gofmt` + `goimports`.
- Linters (golangci-lint): `copyloopvar`, `errcheck`, `errorlint`, `gosec`, `govet`, `ineffassign`, `intrange`, `staticcheck`, `unused`.
- Modules: `readonly` download mode.
- Indent: tabs (size 4).
- Commands:
  ```sh
  go build ./...
  golangci-lint run
  go test -coverprofile=coverage.out ./...
  go tool cover -func=coverage.out
  ```

### Python

- Requires Python >= 3.14.
- Package manager: `uv`.
- Formatter/linter: `ruff` with rules `E`, `F`, `I`, `UP`, `B`, `SIM`; line length 120.
- Indent: 4 spaces.
- Commands:
  ```sh
  uv run ruff check .
  uv run ruff format --check .
  uv run pytest
  ```

### TypeScript / Node

- Node >= 26.0.0. Package manager: `pnpm` (frozen lockfile).
- TypeScript: strict mode, ES2022 target, `isolatedModules: true`, source in `src/`, output to `dist/`.
- Linter/formatter: Biome 2.5.9 recommended ruleset — 2 spaces, line width 120, no semicolons (`asNeeded`), double quotes, trailing commas (`es5`).
- Test runner: Jest + ts-jest; coverage enforced at >= 90%.
- Commands:
  ```sh
  pnpm install --frozen-lockfile
  pnpm exec biome check .
  pnpm exec tsc --noEmit
  pnpm test -- --coverage
  ```

## Branch naming

| Branch | Purpose |
| --- | --- |
| `main` | Stable, production-ready. Protected — no direct commits. |
| `feat/<description>` | New features. |
| `fix/<description>` | Bug fixes. |
| `release/<version>` | Release preparation. |

Pull requests require at least one approving review. CI MUST pass before merge.

## What NOT to do

- Do not add `latest` container image tags.
- Do not commit configuration files that contain credentials.
- Do not introduce new dependencies without justification, license check, and vulnerability scan.
- Do not merge with failing CI or unresolved security findings.