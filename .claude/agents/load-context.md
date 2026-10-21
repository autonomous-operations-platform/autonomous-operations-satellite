---
name: load-context
description: Clones the AO central documentation into .cache/context/ for agent grounding. Run at the start of any session that requires architectural context, ADR decisions, or spec references. Safe to re-run — pulls latest if the clone already exists.
---

# load-context

Populate `.cache/context/` with the AO central documentation so the agent can
search and read it during the session.

## Steps

### 1. Clone or update

If `.cache/context/` does not exist:

```sh
git clone \
  --filter=blob:none \
  --depth=1 \
  https://github.tools.sap/autonomous-operations/autonomous-operations \
  .cache/context
```

If `.cache/context/` already exists:

```sh
git -C .cache/context pull --depth=1
```

### 2. Confirm

Print one line:

```
load-context: .cache/context/ ready (<commit-sha>)
```

Get the sha with `git -C .cache/context rev-parse --short HEAD`.

Do not print directory listings or file counts. Do not read any files.

## Using the context

After this skill runs, `.cache/context/` contains the full AO documentation tree.

**Always search before reading.** Never load a file without locating it first.
Use one of:

```sh
# ripgrep — preferred; searches file contents
rg "term" .cache/context/

# fd — search by filename or path pattern
fd "pattern" .cache/context/

# grep — fallback if rg/fd unavailable
grep -r "term" .cache/context/
```

Read only the files the search identifies as relevant. Use line ranges where the
relevant section is identifiable from the search output.

The context cache is read-only. Never write, edit, or commit files inside `.cache/context/`.