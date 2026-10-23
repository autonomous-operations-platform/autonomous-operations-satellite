---
name: commit
description: Stage and commit changes using Conventional Commits. Enforces commitlint rules, branch naming, and pre-commit checks. Run when ready to commit work.
---

# commit

Stage and commit changes. Follows the commit format enforced by `commitlint` and
the branch naming strategy in `AGENTS.md`.

## Steps

### 1. Verify branch

Check the current branch:

```sh
git branch --show-current
```

The branch MUST follow this naming convention:

| Pattern | Purpose |
| --- | --- |
| `main` | Protected — never commit directly |
| `feat/<description>` | New features |
| `fix/<description>` | Bug fixes |
| `release/<version>` | Release preparation |

If on `main`, **stop** and tell the user to create a branch first. Do not commit.

If the branch name does not follow the convention, warn the user and ask whether
to rename it before continuing. Do not commit until the branch is valid.

### 2. Review staged and unstaged changes

```sh
git status
git diff
git diff --cached
```

Check for:

- Hardcoded credentials, tokens, API keys, or passwords — **stop if found**.
- Debug code, leftover scaffolding, commented-out blocks — flag to the user.
- Files that should not be committed (`.env`, generated files, build artifacts,
  `.cache/`) — do not stage them.

### 3. Determine the commit type and scope

Select `type` and `scope` from the allowed values enforced by `commitlint.config.mjs`:

**Types:** `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `ci`, `build`, `revert`

**Scopes (optional):** `deps`, `devops`, `security`, `deployment`, `architecture`, `ci`, `github`

Omit scope when none of the standard scopes fit or the change spans multiple areas.

Choose the type that best describes the primary intent of the change:

- `feat` — adds new capability visible to users or callers
- `fix` — corrects a defect
- `docs` — documentation only, no code change
- `refactor` — restructures code without changing behavior
- `test` — adds or updates tests only
- `chore` — maintenance: dependency bumps, config, tooling
- `perf` — improves performance without functional change
- `ci` — CI/CD pipeline changes
- `build` — build system or external dependency changes
- `revert` — reverts a prior commit

Choose the scope that best describes the area of the codebase affected.

### 4. Write the commit message

Format:

```
<type>(<scope>): <subject>

<body>

<footer>
```

Rules (enforced by commitlint):

- **Header max length:** 120 characters.
- **Scope is optional.** Use one of the standard scopes when applicable; omit when none fit or the change spans multiple areas.
- **Subject:** lowercase start, no trailing period, no PascalCase or UPPER_CASE.
- **Body:** present tense. Explain *why*, not *what* — the diff shows what changed.
  Wrap at 100 characters.
- **Footer:** reference issues or PRs as `Closes #123` or `Refs #456`. Omit if none.
- **Breaking change:** add `!` after the type or scope — `feat!:` or `feat(api)!:` — and describe the
  breaking change in the footer: `BREAKING CHANGE: <description>`.

PR title rule (enforced by `semantic-pull-requests.yml`): the subject of the PR title
MUST end with `(#123)` where `#123` is the GitHub issue number, or `(NO-TICKET)` if
no issue exists. This applies to the PR title only — not to individual commit messages.

Examples:

```
feat(deps): add retry logic to writer

Transient network errors were causing silent data loss. The writer now
retries up to three times with exponential back-off before returning an error.

Closes #42
```

```
fix(deps): pin grpc to v1.62.1

v1.63.0 introduced a regression in connection pool teardown that caused
test flakiness on shutdown.
```

### 5. Stage and commit

Stage only the files relevant to this change:

```sh
git add <file> [<file> ...]
```

Do not use `git add -A` or `git add .` unless every modified file belongs to this
commit. If multiple logical changes are present, commit them separately.

Commit:

```sh
git commit -m "$(cat <<'EOF'
<type>(<scope>): <subject>

<body>

<footer>
EOF
)"
```

### 6. Handle pre-commit failures

If the pre-commit hook fails:

1. Read the error output — identify which check failed (commitlint, linter, test).
2. Fix the underlying issue. Do not use `--no-verify`.
3. Re-stage and create a **new** commit. Do not amend.

### 7. Confirm

Print one line:

```
commit: <short-sha> <type>(<scope>): <subject>
```

Get the sha with `git rev-parse --short HEAD`.