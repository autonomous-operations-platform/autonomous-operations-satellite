---
name: load-ao-spec
description: Clones the AO central documentation into .cache/ao-spec/ for agent grounding. Run at the start of any session that requires architectural context, ADR decisions, or spec references. Safe to re-run — pulls latest if the clone already exists.
---

# load-ao-spec

Populate `.cache/ao-spec/` with the AO central documentation so the agent can
search and read it during the session.

## Steps

### 1. Clone or update

If `.cache/ao-spec/` does not exist:

```sh
git clone \
  --filter=blob:none \
  --no-checkout \
  --depth=1 \
  https://github.com/autonomous-operations-platform/autonomous-operations \
  .cache/ao-spec

git -C .cache/ao-spec sparse-checkout set content
git -C .cache/ao-spec checkout
```

If `.cache/ao-spec/` already exists:

```sh
git -C .cache/ao-spec pull --depth=1
```

### 2. Confirm

Print one line:

```
load-ao-spec: .cache/ao-spec/ ready (<commit-sha>)
```

Get the sha with `git -C .cache/ao-spec rev-parse --short HEAD`.

Do not print directory listings or file counts. Do not read any files.

## Using the context

After this skill runs, `.cache/ao-spec/` contains the full AO documentation tree.

**Always search before reading.** Never load a file without locating it first.
Use one of:

```sh
# ripgrep — preferred; searches file contents
rg "term" .cache/ao-spec/

# fd — search by filename or path pattern
fd "pattern" .cache/ao-spec/

# grep — fallback if rg/fd unavailable
grep -r "term" .cache/ao-spec/
```

Read only the files the search identifies as relevant. Use line ranges where the
relevant section is identifiable from the search output.

The context cache is read-only. Never write, edit, or commit files inside `.cache/ao-spec/`.
