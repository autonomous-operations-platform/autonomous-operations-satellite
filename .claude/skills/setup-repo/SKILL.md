---
name: setup-repo
description: Configures a freshly cloned AO repository. Run once, immediately after cloning. Asks six questions and appends project-specific sections to AGENTS.md, then writes CODEOWNERS and README.md.
---

# setup-repo

Configure a freshly cloned AO repository. Run this once, immediately after cloning.

## What this skill does

Fills the project-specific sections of `AGENTS.md` and writes:

- `AGENTS.md` — appends project description, directory structure, commands, spec references, rule overrides
- `CODEOWNERS` — team slug for default reviewers
- `README.md` — repository name and description

No other files are modified.

## Steps

### 1. Gather information

Ask the user the following six questions **in a single message**. Do not proceed until all answers are provided.

1. **Repository name** — what is this repository called? (One word or short slug, e.g. `collector-runtime`)
2. **One-line description** — what does this repository do? (One sentence, present tense)
3. **Role in the AO system** — one paragraph describing this component's place in the Autonomous Operations architecture. What does it produce, consume, or coordinate?
4. **Key directories** — list the main directories and what each contains. Format as `dir/ — purpose`. Add as many as needed.
5. **Team slug** — the GitHub team slug for `CODEOWNERS` (e.g. `autonomous-operations/backend`). Check existing repos if unsure.
6. **Spec section URLs** — zero or more GitHub raw URLs to spec sections that govern this repository's implementation. Leave blank if none yet.

### 2. Detect the stack

Before writing files, detect the repository stack by checking for these marker files:

- `go.mod` → Go
- `package.json` → Node / TypeScript
- `pyproject.toml` → Python
- None of the above → generic

Use the detected stack to select the correct build/test/lint commands in step 3.

### 3. Append project sections to AGENTS.md

Append the following block to the end of `AGENTS.md`.
Do **not** leave any `<placeholder>` text in the output.
Do **not** modify any existing content in `AGENTS.md`.

```markdown
---

## This repository

<role-paragraph from question 3>

## Directory structure

<key-directories from question 4, one per line: `dir/ — purpose`>

## Commands

<stack-specific commands — see below>

## Testing

Line coverage MUST be ≥ 90%. Enforced by pre-commit and CI.
Verify every test can fail before committing: flip an assertion or break the code
under test and confirm the test turns red. Revert before committing.

## Spec references

<spec-urls from question 6, one per line as markdown links — omit section if none provided>
```

**Commands by stack:**

Go:
```sh
go build ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
golangci-lint run
```

Node / TypeScript:
```sh
pnpm install --frozen-lockfile
pnpm exec biome check .
pnpm exec tsc --noEmit
pnpm test -- --coverage
```

Python:
```sh
uv run ruff check .
uv run ruff format --check .
uv run pytest
```

### 4. Write CODEOWNERS

Replace the contents of `.github/CODEOWNERS` with:

```
* @<team-slug from question 5>
```

If `CODEOWNERS` does not exist, create it at `.github/CODEOWNERS`.

### 5. Write README.md

Replace the first two lines of `README.md` with:

```markdown
# <repo-name>

<one-line description from question 2>
```

Preserve everything below the first paragraph break (installation instructions, badge lines, etc.). If `README.md` does not exist or is empty, write only the two lines above.

### 6. Confirm

After writing all files, print a single summary line:

```
setup-repo complete: AGENTS.md, .github/CODEOWNERS, README.md
```

Do not print file contents or diffs. Do not make any additional changes.